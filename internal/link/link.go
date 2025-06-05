package link

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	"github.com/ecodeclub/ekit/retry"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/gotomicro/ego/core/elog"
)

var (
	_             gateway.Link = (*link)(nil)
	ErrLinkClosed              = errors.New("websocket: 连接已关闭")
)

type link struct {
	// 基本信息
	id     string
	bizID  int64
	userID int64

	// 连接和配置
	conn              net.Conn
	readTimeout       time.Duration
	writeTimeout      time.Duration
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

	// 日志
	logger *elog.Component
}

type Option func(*link)

func WithTimeouts(read, write time.Duration) Option {
	return func(l *link) {
		l.readTimeout = read
		l.writeTimeout = write
	}
}

func WithRetry(initInterval, maxInterval time.Duration, maxRetries int32) Option {
	return func(l *link) {
		l.initRetryInterval = initInterval
		l.maxRetryInterval = maxInterval
		l.maxRetries = maxRetries
	}
}

func WithBuffer(sendBuf, recvBuf int) Option {
	return func(l *link) {
		l.sendCh = make(chan []byte, sendBuf)
		l.receiveCh = make(chan []byte, recvBuf)
	}
}

func New(parent context.Context, id string, bizID, userID int64, conn net.Conn, opts ...Option) gateway.Link {
	if parent == nil {
		parent = context.Background()
	}

	ctx, cancel := context.WithCancel(parent)
	l := &link{
		id:                id,
		bizID:             bizID,
		userID:            userID,
		conn:              conn,
		readTimeout:       30 * time.Second, // 默认值
		writeTimeout:      10 * time.Second,
		initRetryInterval: time.Second,
		maxRetryInterval:  5 * time.Second,
		maxRetries:        3,
		sendCh:            make(chan []byte, 256), // 默认缓冲
		receiveCh:         make(chan []byte, 256),
		ctx:               ctx,
		cancel:            cancel,
		logger:            elog.EgoLogger.With(elog.FieldComponent("Link")),
	}

	// 应用选项
	for _, opt := range opts {
		opt(l)
	}

	go l.sendLoop()
	go l.receiveLoop()
	return l
}

func (l *link) sendLoop() {
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

func (l *link) sendWithRetry(payload []byte) bool {
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
		err := wsutil.WriteServerBinary(l.conn, payload)
		if err == nil {
			// 发送成功
			return true
		}

		l.logger.Error("向客户端发消息失败",
			elog.String("linkID", l.id),
			elog.Int64("bizID", l.bizID),
			elog.Int64("userID", l.userID),
			elog.Int("payloadLen", len(payload)),
			elog.FieldErr(err),
		)

		// 检查是否为可重试的网络超时错误
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			duration, couldRetry := retryStrategy.Next()
			if !couldRetry {
				l.logger.Error("重试次数耗尽，放弃发送",
					elog.String("linkID", l.id),
					elog.Int64("bizID", l.bizID),
					elog.Int64("userID", l.userID),
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

func (l *link) receiveLoop() {
	defer func() {
		close(l.receiveCh) // 由写端关闭
		_ = l.Close()
	}()

	for {
		// 检查连接状态
		select {
		case <-l.ctx.Done():
			return
		default:
		}

		// 设置读超时并读取消息
		_ = l.conn.SetReadDeadline(time.Now().Add(l.readTimeout))
		// 前端可能难以控制需要使用下面的代码
		// payload, _, err := wsutil.ReadClientData(l.conn)
		payload, err := wsutil.ReadClientBinary(l.conn)
		if err != nil {
			//  检查是否为可重试的网络超时错误
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}

			l.logger.Error("从客户端读取消息失败",
				elog.String("linkID", l.id),
				elog.Int64("bizID", l.bizID),
				elog.Int64("userID", l.userID),
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

func (l *link) ID() string                 { return l.id }
func (l *link) BizID() int64               { return l.bizID }
func (l *link) UserID() int64              { return l.userID }
func (l *link) Receive() <-chan []byte     { return l.receiveCh }
func (l *link) HasClosed() <-chan struct{} { return l.ctx.Done() }

func (l *link) Send(payload []byte) error {
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

func (l *link) Close() error {
	l.closeOnce.Do(func() {
		// 1. 尝试发送 WebSocket 关闭帧（忽略错误），不用 l.writeTimeout 它可能太长了
		_ = l.conn.SetWriteDeadline(time.Now().Add(time.Second))
		_ = wsutil.WriteServerMessage(l.conn, ws.OpClose, nil)

		// 2. 取消 context，通知所有 goroutine
		l.cancel()

		// 3. 关闭发送通道，解除 Send 阻塞
		// 会导致 Send 中 l.sendCh <- payload 分支 panic: send on closed channel 所以选择不关闭
		// close(l.sendCh)

		// 4. 关闭底层连接
		l.closeErr = l.conn.Close()
	})
	return l.closeErr
}
