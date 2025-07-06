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

const (
	sendBatch = 16
)

type LinkV3 struct {
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
	limitQPS int
	limiter  *rate.Limiter // golang.org/x/time/rate 标准扩展库

	// 日志
	logger *elog.Component
}

type OptionV3 func(*LinkV3)

// ---------------- constructor ----------------

func NewV3(parent context.Context, id string, sess session.Session, conn net.Conn, opts ...OptionV3) *LinkV3 {
	ctx, cancel := context.WithCancel(parent)
	l := &LinkV3{
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
		logger:            elog.EgoLogger.With(elog.FieldComponent("LinkV3")),
	}

	// 选项
	for _, opt := range opts {
		opt(l)
	}

	// Reader / Writer
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

func (l *LinkV3) ID() string                 { return l.id }
func (l *LinkV3) Session() session.Session   { return l.sess }
func (l *LinkV3) Receive() <-chan []byte     { return l.receiveCh }
func (l *LinkV3) HasClosed() <-chan struct{} { return l.ctx.Done() }

func (l *LinkV3) Send(payload []byte) error {
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

func (l *LinkV3) Close() error {
	l.closeOnce.Do(func() {
		_ = l.conn.SetWriteDeadline(time.Now().Add(DefaultCloseTimeout))
		_ = wsutil.WriteServerMessage(l.conn, ws.OpClose, nil)

		l.cancel()
		l.closeErr = l.conn.Close()
	})
	return l.closeErr
}

func (l *LinkV3) readWriteLoop() {
	defer func() {
		close(l.receiveCh)
		_ = l.Close()
	}()

	for {
		// ------------- 批量发送 -------------
		for i := 0; i < sendBatch; i++ {
			var outbound []byte
			select {
			case <-l.ctx.Done():
				return
			case outbound = <-l.sendCh:
			default:
				// 没可发送的
				outbound = nil
			}

			if outbound == nil {
				break // 立刻进入读
			}
			retryStrategy, _ := retry.NewExponentialBackoffRetryStrategy(
				l.initRetryInterval, l.maxRetryInterval, l.maxRetries)
			if !l.sendWithRetryInternal(outbound, retryStrategy) {
				return
			}
		}

		// ------------- 限流（非阻塞） -------------
		if l.limiter != nil && !l.limiter.Allow() {
			// 本次读取被限流，直接进入下一轮循环（仍旧有机会发送）
			continue
		}

		// ------------- 读取 -------------
		_ = l.conn.SetReadDeadline(time.Now().Add(l.readTimeout))
		payload, err := l.reader.Read()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				// 读超时，让循环回到顶部处理 send
				continue
			}

			var wsErr wsutil.ClosedError
			if errors.As(err, &wsErr) &&
				(wsErr.Code == ws.StatusNoStatusRcvd || wsErr.Code == ws.StatusGoingAway) {
				l.logger.Info("客户端关闭连接",
					elog.String("linkID", l.id),
					elog.Any("userInfo", l.sess.UserInfo()))
				return
			}

			l.logger.Error("读取客户端消息失败",
				elog.String("linkID", l.id),
				elog.FieldErr(err))
			return
		}

		// 投递给业务
		select {
		case <-l.ctx.Done():
			return
		case l.receiveCh <- payload:
		}
	}
}

func (l *LinkV3) sendWithRetryInternal(payload []byte, rs retry.Strategy) bool {
	for {
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

		l.logger.Error("发送消息失败，准备重试",
			elog.String("linkID", l.id),
			elog.Int("payloadLen", len(payload)),
			elog.FieldErr(err))

		// 只对超时做重试
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			d, ok := rs.Next()
			if !ok {
				l.logger.Error("重试次数耗尽，放弃发送",
					elog.String("linkID", l.id))
				return false
			}
			select {
			case <-l.ctx.Done():
				return false
			case <-time.After(d):
				continue
			}
		}
		return false
	}
}

// ---------------- idle ----------------

func (l *LinkV3) UpdateActiveTime() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() == nil {
		l.lastActiveTime = time.Now()
	}
}

func (l *LinkV3) TryCloseIfIdle(timeout time.Duration) bool {
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
			elog.FieldErr(err))
	}
	return true
}
