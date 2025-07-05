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
	"golang.org/x/time/rate"
)

type LinkV4 struct {
	// 基本信息
	id   string
	sess session.Session

	// 连接
	conn         net.Conn
	reader       *wswrapper.Reader
	writer       *wswrapper.Writer
	readTimeout  time.Duration
	writeTimeout time.Duration

	// 压缩
	compressionState *compression.State

	// 重试
	initRetryInterval time.Duration
	maxRetryInterval  time.Duration
	maxRetries        int32

	// 通信通道
	sendCh    chan []byte
	receiveCh chan []byte

	// 生命周期
	ctx    context.Context
	cancel context.CancelFunc

	// 关闭控制
	closeOnce sync.Once
	closeErr  error

	// 空闲检测
	mu             sync.RWMutex
	autoClose      bool
	lastActiveTime time.Time

	// 限流
	limiter *rate.Limiter

	// 日志
	logger *elog.Component
}

type OptionV4 func(*LinkV4)

// ---------------- Options ----------------

func WithTimeoutsV4(read, write time.Duration) OptionV4 {
	return func(l *LinkV4) {
		l.readTimeout = read
		l.writeTimeout = write
	}
}

func WithCompressionV4(state *compression.State) OptionV4 {
	return func(l *LinkV4) {
		l.compressionState = state
	}
}

func WithRetryV4(initInterval, maxInterval time.Duration, maxRetries int32) OptionV4 {
	return func(l *LinkV4) {
		l.initRetryInterval = initInterval
		l.maxRetryInterval = maxInterval
		l.maxRetries = maxRetries
	}
}

func WithBufferV4(sendBuf, recvBuf int) OptionV4 {
	return func(l *LinkV4) {
		l.sendCh = make(chan []byte, sendBuf)
		l.receiveCh = make(chan []byte, recvBuf)
	}
}

func WithAutoCloseV4(autoClose bool) OptionV4 {
	return func(l *LinkV4) {
		l.autoClose = autoClose
	}
}

func WithRateLimitV4(qps int) OptionV4 {
	return func(l *LinkV4) {
		if qps > 0 {
			l.limiter = rate.NewLimiter(rate.Limit(qps), qps)
		}
	}
}

// ---------------- Constructor ----------------

func NewV4(parent context.Context, id string, sess session.Session, conn net.Conn, opts ...OptionV4) *LinkV4 {
	ctx, cancel := context.WithCancel(parent)
	l := &LinkV4{
		id:                id,
		sess:              sess,
		conn:              conn,
		readTimeout:       DefaultReadTimeout,
		writeTimeout:      DefaultWriteTimeout,
		initRetryInterval: DefaultInitRetryInterval,
		maxRetryInterval:  DefaultMaxRetryInterval,
		maxRetries:        DefaultMaxRetries,
		sendCh:            make(chan []byte, DefaultWriteBufferSize),
		receiveCh:         make(chan []byte, DefaultReadBufferSize),
		ctx:               ctx,
		cancel:            cancel,
		lastActiveTime:    time.Now(),
		logger:            elog.EgoLogger.With(elog.FieldComponent("LinkV4")),
	}

	// 应用选项
	for _, opt := range opts {
		opt(l)
	}

	// 初始化读写器
	l.reader = wswrapper.NewServerSideReader(conn)
	var compress bool
	if l.compressionState != nil {
		compress = l.compressionState.Enabled
	}
	l.writer = wswrapper.NewServerSideWriter(conn, compress)

	// 启动单协程
	go l.readWriteLoop()
	return l
}

// ---------------- Public API ----------------

func (l *LinkV4) ID() string                 { return l.id }
func (l *LinkV4) Session() session.Session   { return l.sess }
func (l *LinkV4) Receive() <-chan []byte     { return l.receiveCh }
func (l *LinkV4) HasClosed() <-chan struct{} { return l.ctx.Done() }

func (l *LinkV4) Send(payload []byte) error {
	select {
	case <-l.ctx.Done():
		return fmt.Errorf("%w", ErrLinkClosed)
	case l.sendCh <- payload:
		// 双重检查，防止在发送到channel后连接恰好关闭
		if l.ctx.Err() != nil {
			return fmt.Errorf("%w", ErrLinkClosed)
		}
		return nil
	}
}

func (l *LinkV4) Close() error {
	l.closeOnce.Do(func() {
		// 发送关闭帧
		_ = l.conn.SetWriteDeadline(time.Now().Add(DefaultCloseTimeout))
		_ = wsutil.WriteServerMessage(l.conn, ws.OpClose, nil)

		// 取消上下文并关闭连接
		l.cancel()
		l.closeErr = l.conn.Close()
	})
	return l.closeErr
}

func (l *LinkV4) readWriteLoop() {
	defer func() {
		close(l.receiveCh) // 只由这个goroutine关闭
		_ = l.Close()
	}()

	// 创建重试策略实例
	retryStrategy, _ := retry.NewExponentialBackoffRetryStrategy(
		l.initRetryInterval, l.maxRetryInterval, l.maxRetries)

	for {
		// ------------- 批量发送阶段 -------------
		sendCount := 0
		for sendCount < sendBatch {
			select {
			case <-l.ctx.Done():
				return
			case payload := <-l.sendCh:
				if !l.sendWithRetryInternal(payload, retryStrategy) {
					return // 发送失败，关闭连接
				}
				sendCount++
			default:
				// 没有更多待发送消息，跳出发送循环
				goto READ_PHASE
			}
		}

	READ_PHASE:
		// ------------- 限流检查 -------------
		if l.limiter != nil && !l.limiter.Allow() {
			// 被限流，跳过本次读取，继续下一轮循环
			continue
		}

		// ------------- 读取阶段 -------------
		_ = l.conn.SetReadDeadline(time.Now().Add(l.readTimeout))
		payload, err := l.reader.Read()

		if err != nil {
			// 检查是否为读超时
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				// 读超时是正常的，继续下一轮循环处理发送
				continue
			}

			// 检查是否为客户端正常关闭
			var wsErr wsutil.ClosedError
			if errors.As(err, &wsErr) &&
				(wsErr.Code == ws.StatusNoStatusRcvd || wsErr.Code == ws.StatusGoingAway) {
				l.logger.Info("客户端关闭连接",
					elog.String("linkID", l.id),
					elog.Any("userInfo", l.sess.UserInfo()))
				return
			}

			// 其他错误，记录并关闭连接
			l.logger.Error("读取客户端消息失败",
				elog.String("linkID", l.id),
				elog.FieldErr(err))
			return
		}

		// ------------- 消息投递阶段 -------------
		// 使用非阻塞发送，防止业务层处理慢导致IO循环阻塞
		select {
		case <-l.ctx.Done():
			return
		case l.receiveCh <- payload:
			// 成功投递消息
		default:
			// 接收通道已满，丢弃消息并记录警告
			// 这是必要的反压保护，防止内存无限增长和IO循环阻塞
			l.logger.Warn("接收通道已满，消息被丢弃，请检查业务处理能力",
				elog.String("linkID", l.id),
				elog.Any("userInfo", l.sess.UserInfo()),
				elog.Int("payloadSize", len(payload)))
		}
	}
}

func (l *LinkV4) sendWithRetryInternal(payload []byte, rs retry.Strategy) bool {
	for {
		select {
		case <-l.ctx.Done():
			return false
		default:
		}

		_ = l.conn.SetWriteDeadline(time.Now().Add(l.writeTimeout))
		_, err := l.writer.Write(payload)
		if err == nil {
			return true // 发送成功
		}

		l.logger.Error("发送消息失败",
			elog.String("linkID", l.id),
			elog.Int("payloadLen", len(payload)),
			elog.FieldErr(err))

		// 只对网络超时错误进行重试
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			duration, canRetry := rs.Next()
			if !canRetry {
				l.logger.Error("重试次数耗尽，放弃发送",
					elog.String("linkID", l.id))
				return false
			}

			// 等待重试间隔
			select {
			case <-l.ctx.Done():
				return false
			case <-time.After(duration):
				continue // 继续重试
			}
		}

		// 非超时错误，直接失败
		return false
	}
}

// ---------------- 活跃时间管理 ----------------

func (l *LinkV4) UpdateActiveTime() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() == nil {
		l.lastActiveTime = time.Now()
	}
}

func (l *LinkV4) TryCloseIfIdle(timeout time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.ctx.Err() != nil {
		return true // 已经关闭
	}
	if !l.autoClose || time.Since(l.lastActiveTime) <= timeout {
		return false // 不需要关闭
	}

	// 执行空闲关闭
	if err := l.Close(); err != nil {
		l.logger.Error("关闭空闲连接失败",
			elog.String("linkID", l.id),
			elog.Any("userInfo", l.sess.UserInfo()),
			elog.FieldErr(err))
	}
	return true
}
