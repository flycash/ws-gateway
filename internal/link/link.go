package link

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"gitee.com/flycash/ws-gateway/pkg/wswrapper"
	"github.com/ecodeclub/ekit/retry"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/gotomicro/ego/core/elog"
	"go.uber.org/ratelimit"
)

var ErrLinkClosed = errors.New("websocket: 连接已关闭")

type Link struct {
	// 基本信息
	id   string
	sess session.Session

	// 连接和配置
	conn         net.Conn
	readTimeout  time.Duration
	writeTimeout time.Duration

	// ws读写器
	reader *wswrapper.Reader
	writer *wswrapper.Writer

	// 压缩相关
	compressionState *compression.State

	// 重试策略
	initRetryInterval time.Duration
	maxRetryInterval  time.Duration
	maxRetries        int32

	// 通信通道
	sendCh    chan []byte
	receiveCh chan []byte

	// 生命周期管理
	ctx    context.Context
	cancel context.CancelFunc

	// 关闭控制
	closeOnce sync.Once
	closeErr  error

	// 空闲连接管理
	mu             sync.RWMutex
	autoClose      bool
	lastActiveTime time.Time

	// 限流
	limitRate int
	limiter   ratelimit.Limiter

	// 日志
	logger *elog.Component
}

type Option func(*Link)

func WithTimeouts(read, write time.Duration) Option {
	return func(l *Link) {
		l.readTimeout = read
		l.writeTimeout = write
	}
}

func WithCompression(state *compression.State) Option {
	return func(l *Link) {
		l.compressionState = state
	}
}

func WithRetry(initInterval, maxInterval time.Duration, maxRetries int32) Option {
	return func(l *Link) {
		l.initRetryInterval = initInterval
		l.maxRetryInterval = maxInterval
		l.maxRetries = maxRetries
	}
}

func WithBuffer(sendBuf, recvBuf int) Option {
	return func(l *Link) {
		l.sendCh = make(chan []byte, sendBuf)
		l.receiveCh = make(chan []byte, recvBuf)
	}
}

func WithAutoClose(autoClose bool) Option {
	return func(l *Link) {
		l.autoClose = autoClose
	}
}

func WithRateLimit(rate int) Option {
	return func(l *Link) {
		if rate > 0 {
			l.limitRate = rate
			l.limiter = ratelimit.New(rate)
		}
	}
}

func New(parent context.Context, id string, sess session.Session, conn net.Conn, opts ...Option) *Link {
	ctx, cancel := context.WithCancel(parent)
	l := &Link{
		id:                id,
		sess:              sess,
		conn:              conn,
		readTimeout:       DefaultReadTimeout,
		writeTimeout:      DefaultWriteTimeout,
		reader:            wswrapper.NewServerSideReader(conn),
		initRetryInterval: DefaultInitRetryInterval,
		maxRetryInterval:  DefaultMaxRetryInterval,
		maxRetries:        DefaultMaxRetries,
		sendCh:            make(chan []byte, DefaultWriteBufferSize),
		receiveCh:         make(chan []byte, DefaultReadBufferSize),
		ctx:               ctx,
		cancel:            cancel,
		lastActiveTime:    time.Now(), // 初始化活跃时间
		logger:            elog.EgoLogger.With(elog.FieldComponent("Link")),
	}

	// 应用选项
	for _, opt := range opts {
		opt(l)
	}

	// 根据 compressionState 初始化 writer
	var compressionEnabled bool
	if l.compressionState != nil {
		compressionEnabled = l.compressionState.Enabled
	}
	l.writer = wswrapper.NewServerSideWriter(conn, compressionEnabled)
	go l.sendLoop()
	go l.receiveLoop()
	return l
}

func (l *Link) sendLoop() {
	defer func() { _ = l.Close() }()

	for {
		select {
		case <-l.ctx.Done():
			return
		case payload, ok := <-l.sendCh:
			if !ok {
				return
			}
			if !l.sendWithRetry(payload) {
				// 发送失败，关闭连接
				return
			}
		}
	}
}

func (l *Link) sendWithRetry(payload []byte) bool {
	retryStrategy, _ := retry.NewExponentialBackoffRetryStrategy(
		l.initRetryInterval, l.maxRetryInterval, l.maxRetries)

	for {
		// 检查连接状态
		select {
		case <-l.ctx.Done():
			return false
		default:
		}

		// 设置写超时并发送
		_ = l.conn.SetWriteDeadline(time.Now().Add(l.writeTimeout))
		// 写入消息内容（wswrapper.Writer 会自动处理压缩和 flush）
		_, err := l.writer.Write(payload)
		if err == nil {
			// 发送成功
			return true
		}

		l.logger.Error("向客户端发消息失败",
			elog.String("linkID", l.id),
			elog.Any("userInfo", l.sess.UserInfo()),
			elog.Int("payloadLen", len(payload)),
			elog.Any("compressionState", l.compressionState),
			elog.FieldErr(err),
		)

		// 检查是否为可重试的网络超时错误
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			duration, couldRetry := retryStrategy.Next()
			if !couldRetry {
				l.logger.Error("重试次数耗尽，放弃发送",
					elog.String("linkID", l.id),
					elog.Any("userInfo", l.sess.UserInfo()),
					elog.Any("compressionState", l.compressionState),
				)
				return false
			}
			// 阻塞一段时间再重试
			select {
			case <-l.ctx.Done():
				return false
			case <-time.After(duration):
				continue
			}
		}

		// 不可重试的错误，直接失败
		return false
	}
}

func (l *Link) receiveLoop() {
	defer func() {
		close(l.receiveCh) // 由写端关闭
		_ = l.Close()
	}()

	for {

		if l.limiter != nil {
			l.limiter.Take()
		}

		// 检查连接状态
		select {
		case <-l.ctx.Done():
			return
		default:
		}

		// 设置读超时并读取消息
		_ = l.conn.SetReadDeadline(time.Now().Add(l.readTimeout))
		// wswrapper.Reader 会自动处理控制帧和解压缩
		payload, err := l.reader.Read()
		if err != nil {
			//  检查是否为可重试的网络超时错误
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}

			var wsErr wsutil.ClosedError
			if errors.As(err, &wsErr) && wsErr.Code == ws.StatusNoStatusRcvd ||
				wsErr.Code == ws.StatusGoingAway {
				l.logger.Info("客户端关闭连接",
					elog.String("linkID", l.id),
					elog.Any("userInfo", l.sess.UserInfo()),
					elog.Any("compressionState", l.compressionState),
				)
				return
			}

			l.logger.Error("从客户端读取消息失败",
				elog.String("linkID", l.id),
				elog.Any("userInfo", l.sess.UserInfo()),
				elog.Any("compressionState", l.compressionState),
				elog.FieldErr(err),
			)
			// 不可重试，退出
			return
		}

		// 成功读取，阻塞发送到接收通道
		select {
		case <-l.ctx.Done():
			return
		case l.receiveCh <- payload:
			// 发送成功，继续下一轮循环
		}
	}
}

func (l *Link) ID() string                 { return l.id }
func (l *Link) Session() session.Session   { return l.sess }
func (l *Link) Receive() <-chan []byte     { return l.receiveCh }
func (l *Link) HasClosed() <-chan struct{} { return l.ctx.Done() }

func (l *Link) Send(payload []byte) error {
	select {
	case <-l.ctx.Done():
		// close(l.sendCh) 多个协程并发调用的时候会panic
		return fmt.Errorf("%w", ErrLinkClosed)
	case l.sendCh <- payload:
		if l.ctx.Err() != nil {
			return fmt.Errorf("%w", ErrLinkClosed)
		}
		return nil
	}
}

func (l *Link) Close() error {
	l.closeOnce.Do(func() {
		// 1. 尝试发送 WebSocket 关闭帧（忽略错误），不用 l.writeTimeout 它可能太长了
		_ = l.conn.SetWriteDeadline(time.Now().Add(DefaultCloseTimeout))
		_ = wsutil.WriteServerMessage(l.conn, ws.OpClose, nil)

		// 2. 取消 context，通知所有 goroutine
		l.cancel()

		// 3. 关闭发送通道，解除 Send 阻塞
		// 会导致 Send 中 l.sendCh <- payload 分支 panic: send on closed channel 所以选择不关闭
		// close(l.sendCh)

		// 4. 关闭底层连接
		l.closeErr = l.conn.Close()

		// 5.销毁session 在部分业务场景下，Close并不代表需要销毁Session，例如在IM中，只有用户退出登录才会销毁Session
		// ctx, cancelFunc := context.WithTimeout(context.Background(), DefaultCloseTimeout)
		// l.closeErr = multierr.Append(l.closeErr, l.sess.Destroy(ctx))
		// cancelFunc()
	})
	return l.closeErr
}

func (l *Link) UpdateActiveTime() {
	l.mu.Lock()
	defer l.mu.Unlock()
	// 未关闭才记录活跃时间
	if l.ctx.Err() == nil {
		l.lastActiveTime = time.Now()
	}
}

func (l *Link) TryCloseIfIdle(timeout time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 因其他原因已关闭了
	if l.ctx.Err() != nil {
		return true
	}
	//  未设置自动关闭或者活跃时间不超过空闲超时，不需要关闭
	if !l.autoClose || time.Since(l.lastActiveTime) <= timeout {
		return false
	}

	// 执行关闭逻辑
	if err := l.Close(); err != nil {
		l.logger.Error("关闭空闲连接失败",
			elog.String("linkID", l.id),
			elog.Any("userInfo", l.sess.UserInfo()),
			elog.FieldErr(err))
	}
	return true
}
