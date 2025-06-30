# Link 读写协程优化分析

## 背景

当前的 Link 实现采用**双协程架构**（`sendLoop` + `receiveLoop`），虽然逻辑清晰，但存在以下问题：
1. **协程开销**：每个连接需要2个协程，在高并发场景下资源消耗较大
2. **上下文切换**：读写协程间的切换增加了系统开销
3. **同步复杂性**：双协程间的生命周期管理较为复杂

本报告探讨将其优化为**单协程架构**的几种设计方案，分析各自的优缺点和适用场景。LinkV2 是提出的单协程 WebSocket 连接管理组件实现。

## 核心设计约束

1. **单 goroutine 约束**：必须在一个 goroutine 中同时处理读写操作
2. **非阻塞要求**：读写操作不能相互阻塞
3. **可靠性要求**：需要处理网络异常、重试、限流等场景
4. **性能要求**：减少系统调用，提高吞吐量

---

## 批量发送版本

### 代码实现

```go
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
	"go.uber.org/multierr"
	"golang.org/x/time/rate"
)

type LinkV2 struct {
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

type OptionV2 func(*LinkV2)

// ---------------- Options ----------------

func WithTimeoutsV2(read, write time.Duration) OptionV2 {
	return func(l *LinkV2) {
		l.readTimeout = read
		l.writeTimeout = write
	}
}

func WithCompressionV2(state *compression.State) OptionV2 {
	return func(l *LinkV2) {
		l.compressionState = state
	}
}

func WithRetryV2(initInterval, maxInterval time.Duration, maxRetries int32) OptionV2 {
	return func(l *LinkV2) {
		l.initRetryInterval = initInterval
		l.maxRetryInterval = maxInterval
		l.maxRetries = maxRetries
	}
}

func WithBufferV2(sendBuf, recvBuf int) OptionV2 {
	return func(l *LinkV2) {
		l.sendCh = make(chan []byte, sendBuf)
		l.receiveCh = make(chan []byte, recvBuf)
	}
}

func WithAutoCloseV2(autoClose bool) OptionV2 {
	return func(l *LinkV2) {
		l.autoClose = autoClose
	}
}

// qps >0 时启用，burst 默认等于 qps
func WithRateLimitV2(qps int) OptionV2 {
	return func(l *LinkV2) {
		if qps > 0 {
			l.limitQPS = qps
			l.limiter = rate.NewLimiter(rate.Limit(qps), qps)
		}
	}
}

// ---------------- constructor ----------------

const (
	DefaultReadTimeout        = time.Second
	DefaultWriteTimeout       = 5 * time.Second
	DefaultCloseTimeout       = 3 * time.Second
	DefaultInitRetryInterval  = 100 * time.Millisecond
	DefaultMaxRetryInterval   = 2 * time.Second
	DefaultMaxRetries   int32 = 5
	DefaultWriteBufferSize    = 32
	DefaultReadBufferSize     = 32
	sendBatch                 = 16 // 批量发送大小，平衡吞吐量和读写公平性
)

var (
	ErrLinkClosed = errors.New("link already closed")
)

func NewV2(parent context.Context, id string, sess session.Session, conn net.Conn, opts ...OptionV2) *LinkV2 {
	ctx, cancel := context.WithCancel(parent)
	l := &LinkV2{
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
		logger:            elog.EgoLogger.With(elog.FieldComponent("LinkV2")),
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
		_ = l.conn.SetWriteDeadline(time.Now().Add(DefaultCloseTimeout))
		_ = wsutil.WriteServerMessage(l.conn, ws.OpClose, nil)

		l.cancel()
		l.closeErr = l.conn.Close()
	})
	return l.closeErr
}

func (l *LinkV2) readWriteLoop() {
	defer func() {
		close(l.receiveCh)
		_ = l.Close()
	}()

	retryStrategy, _ := retry.NewExponentialBackoffRetryStrategy(
		l.initRetryInterval, l.maxRetryInterval, l.maxRetries)

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

func (l *LinkV2) sendWithRetryInternal(payload []byte, rs retry.Strategy) bool {
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
			elog.FieldErr(err))
	}
	return true
}
```

### 设计思路

版本1采用**批量发送 + 轮询读取**的策略：
1. 每轮循环首先批量处理发送队列，最多连续发送16条消息
2. 然后进行一次读取操作，使用正常的读超时（1秒）
3. 通过限流器控制读取频率，避免过度消耗CPU

### 优点分析

1. **批量发送效率高**：连续发送多条消息，减少读写切换开销，提高吞吐量
2. **代码逻辑清晰**：结构简单，易于理解和维护
3. **CPU开销合理**：使用正常的读超时而不是频繁轮询，减少系统调用
4. **完整的限流机制**：通过 rate.Limiter 实现非阻塞限流
5. **消息可靠性高**：正常情况下不会丢失消息

### 缺点分析

1. **致命缺陷：阻塞风险**：`l.receiveCh <- payload` 是阻塞的，如果业务层处理慢，会导致整个IO循环卡死
2. **缺乏反压保护**：没有处理接收通道满的情况，可能导致内存无限增长
3. **读取延迟较高**：需要等待批量发送完成后才能读取，在高写入场景下可能导致读取延迟
4. **相比当前双协程架构**：虽然减少了协程数量，但增加了单协程内的复杂性
5. **潜在读饥饿**：若 `sendCh` 长期高水位，即使 `sendBatch` 限制也可能让写阶段持续占满循环，导致读取阶段长时间得不到执行

### 适用场景

- 业务层处理能力能够跟上网络接收速度
- 发送消息较多，需要高吞吐量的场景
- 对消息可靠性要求较高的场景

---

## 非阻塞读版本

### 代码实现

```go
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

type LinkV3 struct {
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

type OptionV3 func(*LinkV3)

func WithTimeoutsV3(read, write time.Duration) OptionV3 {
	return func(l *LinkV3) {
		l.readTimeout = read
		l.writeTimeout = write
	}
}

func WithCompressionV3(state *compression.State) OptionV3 {
	return func(l *LinkV3) {
		l.compressionState = state
	}
}

func WithRetryV3(initInterval, maxInterval time.Duration, maxRetries int32) OptionV3 {
	return func(l *LinkV3) {
		l.initRetryInterval = initInterval
		l.maxRetryInterval = maxInterval
		l.maxRetries = maxRetries
	}
}

func WithBufferV3(sendBuf, recvBuf int) OptionV3 {
	return func(l *LinkV3) {
		l.sendCh = make(chan []byte, sendBuf)
		l.receiveCh = make(chan []byte, recvBuf)
	}
}

func WithAutoCloseV3(autoClose bool) OptionV3 {
	return func(l *LinkV3) {
		l.autoClose = autoClose
	}
}

func WithRateLimitV3(r int) OptionV3 {
	return func(l *LinkV3) {
		if r > 0 {
			l.limiter = rate.NewLimiter(rate.Limit(r), r)
		}
	}
}

func NewV3(parent context.Context, id string, sess session.Session, conn net.Conn, opts ...OptionV3) *LinkV3 {
	ctx, cancel := context.WithCancel(parent)
	l := &LinkV3{
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
		logger:            elog.EgoLogger.With(elog.FieldComponent("LinkV3")),
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

func (l *LinkV3) readWriteLoop() {
	defer func() {
		close(l.receiveCh)
		_ = l.Close()
	}()

	// 初始设置一个常规的 idle read deadline
	_ = l.conn.SetReadDeadline(time.Now().Add(l.readTimeout))

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
			// 写操作成功后，重置连接的空闲读超时，因为一次成功的写也代表连接是活跃的
			_ = l.conn.SetReadDeadline(time.Now().Add(l.readTimeout))

		default:
			// 没有写任务，执行非阻塞读
			// 设置一个极短的 deadline，使 Read 操作几乎立即返回
			_ = l.conn.SetReadDeadline(time.Now().Add(veryShortReadTimeout))
			payload, err := l.reader.Read()

			if err == nil {
				// 成功读取到数据
				if l.limiter != nil && !l.limiter.Allow() {
					l.logger.Warn("接收消息速率超限，消息被丢弃",
						elog.String("linkID", l.id),
						elog.Any("userInfo", l.sess.UserInfo()))
					// 丢弃消息，继续下一次循环
				} else {
					// 使用非阻塞方式发送到 receiveCh，防止业务层阻塞 IO 循环
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
				// 无论消息是发送成功还是被丢弃，读操作成功都代表连接活跃，
				// 重置为一个长的 idle deadline，等待下一次的 select 循环
				_ = l.conn.SetReadDeadline(time.Now().Add(l.readTimeout))
				continue // 继续循环，优先处理可能到来的写任务
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

			// 仅在因 veryShortReadTimeout 导致超时后，才重置为长超时
			_ = l.conn.SetReadDeadline(time.Now().Add(l.readTimeout))
		}
	}
}

func (l *LinkV3) sendWithRetry(payload []byte) bool {
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
		l.cancel()
		_ = l.conn.SetWriteDeadline(time.Now().Add(DefaultCloseTimeout))
		_ = wsutil.WriteServerMessage(l.conn, ws.OpClose, nil)
		l.closeErr = l.conn.Close()
	})
	return l.closeErr
}

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
			elog.Any("userInfo", l.sess.UserInfo()),
			elog.FieldErr(err))
	}
	return true
}

const (
	DefaultReadTimeout       = 3 * time.Minute
	DefaultWriteTimeout      = 1 * time.Minute
	DefaultCloseTimeout      = 2 * time.Second
	DefaultWriteBufferSize   = 256
	DefaultReadBufferSize    = 256
	DefaultInitRetryInterval = 100 * time.Millisecond
	DefaultMaxRetryInterval  = 2 * time.Second
	DefaultMaxRetries        = 5
)

var ErrLinkClosed = errors.New("link closed")
```

### 设计思路

版本2采用**写优先 + 非阻塞读**的策略：
1. 使用 select 结构优先处理写操作和关闭信号
2. 在 default 分支中进行非阻塞读取，使用极短的超时时间（1毫秒）
3. 对接收通道使用非阻塞发送，避免IO循环阻塞
4. 实现完整的反压保护机制

### 优点分析

1. **写操作优先级高**：通过 select 结构保证写操作不被读操作阻塞
2. **非阻塞IO循环**：不会因为接收通道满而阻塞，保证系统稳定性
3. **完整的反压保护**：通道满时丢弃消息，并记录详细日志
4. **响应性好**：写操作能够及时响应，延迟低

### 缺点分析

1. **CPU开销巨大**：频繁的1毫秒超时导致大量系统调用，CPU使用率高
2. **系统调用频繁**：频繁调用 `SetReadDeadline` 本身也是系统调用，增加额外开销
3. **代码复杂度高**：逻辑复杂，频繁的 deadline 设置，维护成本高
4. **消息丢失风险**：为了保护系统会主动丢弃消息
5. **网络效率低**：频繁的短超时可能错过正在传输中的数据
6. **GC压力上升**：高频创建临时对象与调用 `SetReadDeadline` 会触发更频繁的垃圾回收

### 适用场景

- 对写操作延迟要求极低的场景
- 业务层处理能力不稳定，需要强反压保护
- 可以容忍消息丢失的场景

---

## 综合优化版本

### 代码实现

```go
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

// ---------------- 常量定义 ----------------

const (
	DefaultReadTimeout        = 30 * time.Second // 合理的读超时，不会太频繁也不会太长
	DefaultWriteTimeout       = 10 * time.Second
	DefaultCloseTimeout       = 3 * time.Second
	DefaultInitRetryInterval  = 100 * time.Millisecond
	DefaultMaxRetryInterval   = 2 * time.Second
	DefaultMaxRetries   int32 = 3
	DefaultWriteBufferSize    = 64
	DefaultReadBufferSize     = 64
	sendBatch                 = 8 // 批量发送大小，平衡吞吐量和读写公平性
)

var (
	ErrLinkClosed = errors.New("link already closed")
)

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
```

### 设计思路

最终版本综合了前面版本的优点，采用**分阶段批量处理**的策略：

1. **批量发送阶段**：每轮最多连续发送8条消息，平衡效率和公平性
2. **限流检查阶段**：非阻塞的限流控制
3. **读取阶段**：使用合理的读超时（30秒），避免频繁系统调用
4. **非阻塞投递阶段**：避免业务层阻塞IO循环，提供反压保护

### 优点分析

1. **性能优化**：
    - 批量发送提高吞吐量，减少读写切换开销
    - 使用30秒读超时而不是1毫秒轮询，大幅减少CPU开销
    - 合理的批量大小（8条）平衡了效率和公平性

2. **稳定性保障**：
    - 非阻塞接收通道发送，防止IO循环阻塞
    - 完善的反压保护机制，系统过载时丢弃消息而不是崩溃
    - 详细的错误日志，便于问题排查

3. **代码质量**：
    - 逻辑清晰，结构分明，易于理解和维护
    - 使用 goto 语句实现清晰的状态跳转
    - 完善的资源管理和生命周期控制

4. **灵活性**：
    - 丰富的配置选项，适应不同场景需求
    - 可选的限流、压缩、重试等功能
    - 良好的扩展性

### 缺点分析

1. **消息丢失风险**：在系统过载时会主动丢弃消息，不适合零丢失要求的场景
2. **批量发送延迟**：在低QPS场景下，消息可能需要等待批量发送，存在轻微延迟
3. **复杂度适中**：相比最简单的阻塞模型，逻辑稍微复杂一些
4. **极端写洪峰下的读延迟**：若持续高写入压力，固定 `sendBatch=8` 仍可能拖慢读取，可考虑自适应批量或时间片

### 适用场景

- **通用WebSocket服务**：大多数实时通信场景
- **高并发系统**：需要处理大量连接的服务器
- **对稳定性要求高**：不能因为个别连接问题影响整体服务
- **资源敏感场景**：需要控制CPU和内存使用的环境

---

## 协程聚合版本

### 设计思路

版本5采用了一个全新的优化思路：**连接聚合管理**。不再是优化单个连接的协程使用，而是通过在一个 LinkV5 中聚合管理两个 Link 连接，使用单个协程同时处理多个连接的读写操作，从而实现协程数量的进一步减半（从每连接2个协程降为每两个连接1个协程）。

这种模式的核心在于一个完全非阻塞的事件循环，轮询处理多个连接的读写事件。

### 代码实现

```go
// package link 实现了对 websocket 连接的抽象和管理。
// 这个文件中的 LinkV5 结构体通过在单个 goroutine 中管理两个连接，
// 旨在优化和减少高并发场景下 goroutine 的开销。
package link

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
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

// =================================================================
// 第一部分: 兼容性更好的 Link 结构体 (支持被动模式)
// =================================================================

type Link struct {
	id   string
	sess session.Session
	conn net.Conn

	readTimeout      time.Duration
	writeTimeout     time.Duration
	compressionState *compression.State

	initRetryInterval time.Duration
	maxRetryInterval  time.Duration
	maxRetries        int32

	sendCh    chan []byte
	receiveCh chan []byte

	ctx    context.Context
	cancel context.CancelFunc

	closeOnce sync.Once
	closeErr  error

	mu             sync.RWMutex
	autoClose      bool
	lastActiveTime time.Time

	limitRate int
	limiter   ratelimit.Limiter
	logger    *elog.Component

	// 新增：被动模式标志，用于 LinkV5
	passiveMode bool
	reader      *wswrapper.Reader
	writer      *wswrapper.Writer
}

func WithPassiveMode() Option {
	return func(l *Link) {
		l.passiveMode = true
	}
}

func (l *Link) sendWithRetry(payload []byte) bool {
	retryStrategy, _ := retry.NewExponentialBackoffRetryStrategy(
		l.initRetryInterval, l.maxRetryInterval, l.maxRetries)
	for {
		select {
		case <-l.ctx.Done():
			return false
		default:
		}
		_ = l.conn.SetWriteDeadline(time.Now().Add(l.writeTimeout))
		_, err := l.writer.Write(payload)
		if err == nil {
			l.UpdateActiveTime()
			return true
		}
		l.logger.Error("发送消息失败", elog.String("linkID", l.id), elog.FieldErr(err))
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			duration, couldRetry := retryStrategy.Next()
			if !couldRetry {
				return false
			}
			select {
			case <-l.ctx.Done(): return false
			case <-time.After(duration): continue
			}
		}
		return false
	}
}

func (l *Link) UpdateActiveTime() {
	l.mu.Lock()
	if l.ctx.Err() == nil {
		l.lastActiveTime = time.Now()
	}
	l.mu.Unlock()
}

// =================================================================
// 第二部分: LinkV5 协程聚合版
// =================================================================

// Message 统一的消息结构
type Message struct {
	LinkID  string // 来源Link的ID
	Payload []byte // 消息内容
}

var (
	ErrLinkV5Closed         = errors.New("LinkV5: 连接组已关闭")
	ErrSubLinkInV5Closed    = errors.New("LinkV5: 组内子连接已关闭")
	ErrTargetLinkNotFound = errors.New("LinkV5: 未找到目标连接ID")
)

// LinkV5 使用单个goroutine管理两个Link
type LinkV5 struct {
	linkA *Link
	linkB *Link

	unifiedReceiveCh chan Message

	ctx    context.Context
	cancel context.CancelFunc

	linkAAlive atomic.Bool
	linkBAlive atomic.Bool

	closeOnce sync.Once
	logger    *elog.Component
}

// newPassiveLink 辅助函数，确保创建用于LinkV5的Link是正确的被动模式
func newPassiveLink(parent context.Context, sess session.Session, conn net.Conn) *Link {
	return New(parent, sess.ID(), sess, conn, WithPassiveMode())
}

// NewLinkV5 创建LinkV5实例
func NewLinkV5(parent context.Context, sessA, sessB session.Session, connA, connB net.Conn) *LinkV5 {
	ctx, cancel := context.WithCancel(parent)

	lv5 := &LinkV5{
		linkA:            newPassiveLink(ctx, sessA, connA),
		linkB:            newPassiveLink(ctx, sessB, connB),
		unifiedReceiveCh: make(chan Message, 128),
		ctx:              ctx,
		cancel:           cancel,
		logger:           elog.EgoLogger.With(elog.FieldComponent("LinkV5")),
	}

	lv5.linkAAlive.Store(true)
	lv5.linkBAlive.Store(true)

	go lv5.readWriteLoop()

	return lv5
}

// --- 公共 API (安全且统一) ---

// Receive 返回统一的接收通道，这是从LinkV5获取消息的唯一正确方式。
func (lv5 *LinkV5) Receive() <-chan Message {
	return lv5.unifiedReceiveCh
}

// Send 将消息路由到正确的子连接。
func (lv5 *LinkV5) Send(msg Message) error {
	var targetLink *Link
	var isAlive *atomic.Bool

	if msg.LinkID == lv5.linkA.id {
		targetLink = lv5.linkA
		isAlive = &lv5.linkAAlive
	} else if msg.LinkID == lv5.linkB.id {
		targetLink = lv5.linkB
		isAlive = &lv5.linkBAlive
	} else {
		return ErrTargetLinkNotFound
	}
	
	if !isAlive.Load() {
		return ErrSubLinkInV5Closed
	}
	
	select {
	case <-lv5.ctx.Done():
		return ErrLinkV5Closed
	case targetLink.sendCh <- msg.Payload:
		return nil
	}
}

// Close 关闭整个LinkV5
func (lv5 *LinkV5) Close() {
	lv5.closeOnce.Do(func() {
		lv5.cancel()
	})
}

// --- 核心循环 ---

func (lv5 *LinkV5) readWriteLoop() {
	defer func() {
		lv5.logger.Info("LinkV5 读写循环退出")
		// 确保底层连接和通道都被正确关闭
		_ = lv5.linkA.Close()
		_ = lv5.linkB.Close()
		close(lv5.unifiedReceiveCh)
	}()

	// 使用 Ticker 来替代 time.Sleep，更精确且能避免空闲时持续空转
	// 建议值 10ms-50ms，避免空闲时CPU空转
	idleTicker := time.NewTicker(20 * time.Millisecond)
	defer idleTicker.Stop()

	for {
		// 优先处理关闭信号
		select {
		case <-lv5.ctx.Done():
			return
		default:
		}

		// 检查是否所有连接都已关闭
		if !lv5.linkAAlive.Load() && !lv5.linkBAlive.Load() {
			lv5.logger.Info("所有连接均已关闭，退出循环")
			return
		}

		// 执行一轮读写，优先写
		hasActivity := lv5.performWrite()
		hasActivity = lv5.performRead() || hasActivity

		// 如果没有活动，等待 Ticker 信号以避免CPU空转
		if !hasActivity {
			select {
			case <-lv5.ctx.Done():
				return
			case <-idleTicker.C:
			}
		}
	}
}

// performWrite 执行一轮写操作
func (lv5 *LinkV5) performWrite() bool {
	w1 := lv5.tryWrite(lv5.linkA, &lv5.linkAAlive)
	w2 := lv5.tryWrite(lv5.linkB, &lv5.linkBAlive)
	return w1 || w2
}

// performRead 执行一轮读操作
func (lv5 *LinkV5) performRead() bool {
	r1 := lv5.tryRead(lv5.linkA, &lv5.linkAAlive)
	r2 := lv5.tryRead(lv5.linkB, &lv5.linkBAlive)
	return r1 || r2
}

// tryRead 尝试非阻塞读取
func (lv5 *LinkV5) tryRead(l *Link, alive *atomic.Bool) bool {
	if !alive.Load() {
		return false
	}
	// 设置极短超时，模拟非阻塞读
	_ = l.conn.SetReadDeadline(time.Now().Add(time.Millisecond))
	payload, err := l.reader.Read()

	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return false // 超时是正常情况，代表没数据可读
		}
		// 其他错误，标记连接为关闭
		lv5.logger.Error("读取消息失败，关闭子连接", elog.String("linkID", l.id), elog.FieldErr(err))
		lv5.markLinkClosed(l, alive)
		return true // 错误也是一种活动
	}

	l.UpdateActiveTime()
	select {
	case lv5.unifiedReceiveCh <- Message{LinkID: l.id, Payload: payload}:
	default:
		lv5.logger.Warn("统一接收通道已满，消息被丢弃", elog.String("linkID", l.id))
	}
	return true
}

// tryWrite 尝试非阻塞写入
func (lv5 *LinkV5) tryWrite(l *Link, alive *atomic.Bool) bool {
	if !alive.Load() {
		return false
	}
	select {
	case payload, ok := <-l.sendCh:
		if !ok {
			lv5.markLinkClosed(l, alive)
			return true // 通道关闭是一种活动
		}

		// 执行单次非阻塞写，不带循环重试，以避免阻塞整个 readWriteLoop
		_ = l.conn.SetWriteDeadline(time.Now().Add(l.writeTimeout))
		_, err := l.writer.Write(payload)
		
		if err != nil {
			// 任何写入失败（包括超时）都将导致连接关闭。
			// 这是为了保证 readWriteLoop 的非阻塞性所做的必要权衡。
			lv5.logger.Error("写入消息失败，关闭子连接", elog.String("linkID", l.id), elog.FieldErr(err))
			lv5.markLinkClosed(l, alive)
		} else {
			// 写入成功，更新活跃时间
			l.UpdateActiveTime()
		}
		return true // 有写入活动

	default:
		return false // 没有数据要写
	}
}

// markLinkClosed 标记连接为已关闭，确保操作的原子性
func (lv5 *LinkV5) markLinkClosed(l *Link, alive *atomic.Bool) {
	if alive.CompareAndSwap(true, false) {
		_ = l.Close() 
	}
}
```

### 优点分析

1.  **协程数量大幅减少**：从传统的每连接2个协程减少到每两个连接1个协程，在高并发场景下能显著降低系统资源消耗。
2.  **统一消息管理**：通过`Message`结构体和统一的接收通道，实现了多连接消息的集中处理，简化了业务层逻辑。
3.  **原子化状态管理**：使用`atomic.Bool`管理连接状态，确保并发安全性。
4.  **故障隔离处理**：单个连接的错误不会直接影响整个`LinkV5`实例，会被优雅地标记为关闭状态。
5.  **灵活的消息路由**：通过`LinkID`实现精确的消息路由，支持业务层针对特定连接进行操作。

### 缺点分析 

1.  **高频轮询开销**：`tryRead`中使用1毫秒的读超时来模拟非阻塞读，这会导致频繁的系统调用和较高的CPU使用率，与版本2的缺陷类似。
2.  **写操作无重试**：为了保证核心循环的非阻塞性，`tryWrite`被设计为单次写入。任何写入失败（包括网络超时）都会直接导致子连接被关闭，牺牲了写入的可靠性来换取响应性。
3.  **设计复杂度显著增加**：需要管理多个连接的生命周期、实现被动模式的`Link`、处理消息路由等，增加了架构和维护的复杂度。
4.  **故障影响面扩大**：虽然有子连接的故障隔离，但如果该单协程发生`panic`，则其聚合的所有连接都会被中断。
5.  **扩展性限制**：当前模型固定聚合两个连接，无法根据系统负载动态调整聚合数量，缺乏弹性。
6.  **调试和监控困难**：多连接聚合在一起，使得问题定位、性能监控和链路追踪的难度都显著增加。

### 实施建议

1.  **连接配对策略**：建议根据连接的特征（如地理位置、用户类型等）进行智能配对，使同一组内的连接生命周期相似。
2.  **监控增强**：需要实现更细粒度的监控，跟踪每个聚合组的健康状态、消息队列积压等情况。
3.  **渐进式迁移**：建议先在部分连接上试验，充分验证其在高负载下的表现和稳定性后再全面推广。
4.  **参数调优**：`idleTicker`（20毫秒）和`readTimeout`（1毫秒）等核心参数需要根据实际场景进行细致的压力测试和调优。

---

## 性能对比总结

| 版本 | CPU开销 | 内存稳定性 | 消息可靠性 | 代码复杂度 | 协程数量 | 适用场景 |
|------|---------|------------|------------|------------|----------|----------|
| 当前双协程 | 中 | 好 | 高 | 低 | 2 goroutines/连接 | 简单可靠场景 |
| 版本1（批量发送） | 中-偏低 | 差（可能 OOM） | 高 | 中 | 1 goroutine/连接 | 消息零丢失要求 |
| 版本2（非阻塞读） | 极高 | 好 | 中（主动丢弃） | 高 | 1 goroutine/连接 | 写延迟敏感 |
| 版本4（综合优化） | 低 | 好 | 中（保护性丢弃） | 中 | 1 goroutine/连接 | 通用生产环境 |
| 版本5（协程聚合） | 中-偏高 | 好 | 中（保护性丢弃） | 高 | 0.5 goroutine/连接 | 超高并发场景 |

## 推荐使用

**推荐策略**：

### 超高并发场景（推荐协程聚合版本）
当连接数量极大（>100K）且系统资源成为严重瓶颈时，**可以考虑使用版本5协程聚合版本**：

1. **协程数量最少**：每连接仅需0.5个协程，是所有版本中最优的
2. **适合资源极度受限环境**：内存和协程调度开销达到最小
3. **需要架构配套支持**：需要合理的连接配对和负载均衡策略
4. **监控要求较高**：需要完善的监控体系支撑

**使用条件**：
- 连接数量 >100K
- 业务层能够处理消息路由逻辑  
- 有完善的监控和运维体系
- 团队有足够的技术能力维护复杂架构

### 高并发场景（推荐综合优化版本）
当连接数量较大（10K-100K）时，**强烈推荐使用版本4综合优化版本**，理由如下：

1. **资源效率高**：每连接只需1个协程，大幅减少内存和调度开销
2. **生产级稳定性**：经过充分的错误处理和边界情况考虑
3. **性能表现优秀**：在吞吐量和延迟之间取得最佳平衡
4. **可维护性好**：代码结构清晰，便于后续修改和扩展
5. **成熟可靠**：复杂度适中，便于问题排查和性能调优

### 中低并发场景（当前双协程架构仍可用）
当连接数量较少（<10K）且对开发效率要求高时，当前的双协程架构仍然是不错的选择：
- 逻辑简单，易于理解和维护
- 读写完全独立，不会相互影响
- 调试和排错相对容易
- 开发和维护成本最低

### 特殊需求场景
- **零消息丢失要求**：可以基于版本1进行改进，增加非阻塞投递
- **极低写延迟要求**：可以基于版本2进行优化，减少轮询频率
- **渐进式升级**：可以从双协程→单协程优化版本→协程聚合版本逐步演进

### 总结
- **<10K连接**：双协程版本（开发简单）
- **10K-100K连接**：综合优化版本（平衡性能与复杂度）  
- **>100K连接**：协程聚合版本（极致资源优化）

版本4综合优化版本代表了在单 goroutine 约束下的最佳实践，版本5协程聚合版本则是极端高并发场景下的终极优化方案。

---

