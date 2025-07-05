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

// 定义一个非常短的读取超时时间，用于模拟非阻塞读取
const veryShortReadTimeout = time.Millisecond

type LinkV2 struct {
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
	limiter *rate.Limiter

	// 日志
	logger *elog.Component
}

type OptionV2 func(*LinkV2)

func NewV2(parent context.Context, id string, sess session.Session, conn net.Conn, opts ...OptionV2) *LinkV2 {
	ctx, cancel := context.WithCancel(parent)
	l := &LinkV2{
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
		lastActiveTime:    time.Now(),
		logger:            elog.EgoLogger.With(elog.FieldComponent("LinkV2")),
	}

	for _, opt := range opts {
		opt(l)
	}

	var compressionEnabled bool
	if l.compressionState != nil {
		compressionEnabled = l.compressionState.Enabled
	}
	l.writer = wswrapper.NewServerSideWriter(conn, compressionEnabled)

	go l.readWriteLoop()

	return l
}

func (l *LinkV2) readWriteLoop() {
	defer func() {
		close(l.receiveCh)
		_ = l.Close()
	}()

	for {
		// select 保证了对写操作、关闭信号的优先响应。
		// 只有在没有写任务和关闭信号时，才会在 default 分支中尝试"读"。
		select {
		case <-l.ctx.Done():
			return

		case payload, ok := <-l.sendCh:
			if !ok {
				return
			}
			// 执行写操作，该函数内部会处理自己的写超时
			if !l.sendWithRetry(payload) {
				return
			}
		default:
			// 没有写任务，执行非阻塞读
			// 设置一个极短的 deadline，使 Read 操作几乎立即返回
			_ = l.conn.SetReadDeadline(time.Now().Add(veryShortReadTimeout))
			payload, err := l.reader.Read()

			if err == nil {
				// 成功读取到数据
				select {
				case l.receiveCh <- payload:
					// 成功发送
				default:
					// receiveCh 满了，丢弃消息，这是必要的反压保护
					l.logger.Warn("接收通道已满，消息被丢弃",
						elog.String("linkID", l.id),
						elog.Any("userInfo", l.sess.UserInfo()))
				}
			}

			// 检查读取错误
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				// 这是预料之中的 "timeout" 错误，意味着当前没有数据可读。
				// 我们忽略这个错误，重置为长 deadline 后继续 select 等待。
			} else {
				// 发生了真正的、不可恢复的错误
				var wsErr wsutil.ClosedError
				if errors.As(err, &wsErr) && (wsErr.Code == ws.StatusNoStatusRcvd || wsErr.Code == ws.StatusGoingAway) {
					l.logger.Info("客户端关闭连接",
						elog.String("linkID", l.id),
						elog.Any("userInfo", l.sess.UserInfo()),
					)
				} else {
					l.logger.Error("从客户端读取消息失败",
						elog.String("linkID", l.id),
						elog.Any("userInfo", l.sess.UserInfo()),
						elog.FieldErr(err),
					)
				}
				return // 退出循环
			}
		}
	}
}

func (l *LinkV2) sendWithRetry(payload []byte) bool {
	retryStrategy, _ := retry.NewExponentialBackoffRetryStrategy(
		l.initRetryInterval, l.maxRetryInterval, l.maxRetries)

	for i := int32(0); i <= l.maxRetries; i++ {
		select {
		case <-l.ctx.Done():
			return false
		default:
		}

		_ = l.conn.SetWriteDeadline(time.Now().Add(l.writeTimeout))
		_, err := l.writer.Write(payload)
		if err == nil {
			return true
		}

		l.logger.Error("向客户端发消息失败",
			elog.String("linkID", l.id),
			elog.Int("payloadLen", len(payload)),
			elog.FieldErr(err),
		)

		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			duration, couldRetry := retryStrategy.Next()
			if !couldRetry {
				break
			}
			select {
			case <-l.ctx.Done():
				return false
			case <-time.After(duration):
				continue
			}
		}
		return false
	}

	l.logger.Error("重试次数耗尽，放弃发送",
		elog.String("linkID", l.id),
		elog.Any("userInfo", l.sess.UserInfo()),
	)
	return false
}

func (l *LinkV2) ID() string                 { return l.id }
func (l *LinkV2) Session() session.Session   { return l.sess }
func (l *LinkV2) Receive() <-chan []byte     { return l.receiveCh }
func (l *LinkV2) HasClosed() <-chan struct{} { return l.ctx.Done() }

func (l *LinkV2) Send(payload []byte) error {
	select {
	case <-l.ctx.Done():
		return fmt.Errorf("%w", ErrLinkClosed)
	case l.sendCh <- payload:
		if l.ctx.Err() != nil {
			return fmt.Errorf("%w", ErrLinkClosed)
		}
		return nil
	}
}

func (l *LinkV2) Close() error {
	l.closeOnce.Do(func() {
		l.cancel()
		_ = l.conn.SetWriteDeadline(time.Now().Add(DefaultCloseTimeout))
		_ = wsutil.WriteServerMessage(l.conn, ws.OpClose, nil)
		l.closeErr = l.conn.Close()
	})
	return l.closeErr
}

func (l *LinkV2) UpdateActiveTime() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() == nil {
		l.lastActiveTime = time.Now()
	}
}

func (l *LinkV2) TryCloseIfIdle(timeout time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil {
		return true
	}
	if !l.autoClose || time.Since(l.lastActiveTime) <= timeout {
		return false
	}
	if err := l.Close(); err != nil {
		l.logger.Error("关闭空闲连接失败",
			elog.String("linkID", l.id),
			elog.Any("userInfo", l.sess.UserInfo()),
			elog.FieldErr(err))
	}
	return true
}
