// package link 实现了对 websocket 连接的抽象和管理。
// 这个文件中的 LinkV4 结构体通过在单个 goroutine 中管理两个连接，
// 旨在优化和减少高并发场景下 goroutine 的开销。
package link

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotomicro/ego/core/elog"
)

// =================================================================
// 第二部分: LinkV4 协程聚合版
// =================================================================

// Message 统一的消息结构
type Message struct {
	LinkID  string // 来源Link的ID
	Payload []byte // 消息内容
}

var (
	ErrLinkV4Closed       = errors.New("LinkV4: 连接组已关闭")
	ErrSubLinkInV5Closed  = errors.New("LinkV4: 组内子连接已关闭")
	ErrTargetLinkNotFound = errors.New("LinkV4: 未找到目标连接ID")
)

// LinkV4 使用单个goroutine管理两个Link
type LinkV4 struct {
	linkA *Link
	linkB *Link

	unifiedReceiveCh chan Message

	ctx    context.Context
	cancel context.CancelFunc

	linkAAlive *atomic.Bool
	linkBAlive *atomic.Bool

	closeOnce sync.Once
	logger    *elog.Component
}

// NewLinkV4 创建LinkV4实例
func NewLinkV4(parent context.Context, linkA, linkB *Link) *LinkV4 {
	ctx, cancel := context.WithCancel(parent)

	lv5 := &LinkV4{

		linkA:            linkA,
		linkB:            linkB,
		unifiedReceiveCh: make(chan Message, 128),
		ctx:              ctx,
		cancel:           cancel,
		linkBAlive:       &atomic.Bool{},
		linkAAlive:       &atomic.Bool{},
		logger:           elog.EgoLogger.With(elog.FieldComponent("LinkV4")),
	}

	lv5.linkAAlive.Store(linkA != nil)
	lv5.linkBAlive.Store(linkB != nil)

	go lv5.readWriteLoop()

	return lv5
}

func (lv4 *LinkV4) SetLinkB(l *Link) {
	lv4.linkB = l
}

// --- 公共 API (安全且统一) ---

// Receive 返回统一的接收通道，这是从LinkV4获取消息的唯一正确方式。
func (lv4 *LinkV4) Receive() <-chan Message {
	return lv4.unifiedReceiveCh
}

// Send 将消息路由到正确的子连接。
func (lv4 *LinkV4) Send(msg Message) error {
	var targetLink *Link
	var isAlive *atomic.Bool

	if lv4.linkAAlive.Load() && msg.LinkID == lv4.linkA.id {
		targetLink = lv4.linkA
		isAlive = lv4.linkAAlive
	} else if lv4.linkBAlive.Load() && msg.LinkID == lv4.linkB.id {
		targetLink = lv4.linkB
		isAlive = lv4.linkBAlive
	} else {
		return ErrTargetLinkNotFound
	}

	if !isAlive.Load() {
		return ErrSubLinkInV5Closed
	}

	select {
	case <-lv4.ctx.Done():
		return ErrLinkV4Closed
	case targetLink.sendCh <- msg.Payload:
		return nil
	}
}

// Close 关闭整个LinkV4
func (lv4 *LinkV4) Close() {
	lv4.closeOnce.Do(func() {
		lv4.cancel()
	})
}

// --- 核心循环 ---

func (lv4 *LinkV4) readWriteLoop() {
	defer func() {
		lv4.logger.Info("LinkV4 读写循环退出")
		// 确保底层连接和通道都被正确关闭
		_ = lv4.linkA.Close()
		_ = lv4.linkB.Close()
		close(lv4.unifiedReceiveCh)
	}()

	// 使用 Ticker 来替代 time.Sleep，更精确且能避免空闲时持续空转
	// 建议值 10ms-50ms，避免空闲时CPU空转
	idleTicker := time.NewTicker(20 * time.Millisecond)
	defer idleTicker.Stop()

	for {
		// 优先处理关闭信号
		select {
		case <-lv4.ctx.Done():
			return
		default:
		}

		// 检查是否所有连接都已关闭
		if !lv4.linkAAlive.Load() && !lv4.linkBAlive.Load() {
			lv4.logger.Info("所有连接均已关闭，退出循环")
			return
		}

		// 执行一轮读写，优先写
		hasActivity := lv4.performWrite()
		hasActivity = lv4.performRead() || hasActivity

		// 如果没有活动，等待 Ticker 信号以避免CPU空转
		if !hasActivity {
			select {
			case <-lv4.ctx.Done():
				return
			case <-idleTicker.C:
			}
		}
	}
}

// performWrite 执行一轮写操作
func (lv4 *LinkV4) performWrite() bool {
	w1 := lv4.tryWrite(lv4.linkA, lv4.linkAAlive)
	w2 := lv4.tryWrite(lv4.linkB, lv4.linkBAlive)
	return w1 || w2
}

// performRead 执行一轮读操作
func (lv4 *LinkV4) performRead() bool {
	r1 := lv4.tryRead(lv4.linkA, lv4.linkAAlive)
	r2 := lv4.tryRead(lv4.linkB, lv4.linkBAlive)
	return r1 || r2
}

// tryRead 尝试非阻塞读取
func (lv4 *LinkV4) tryRead(l *Link, alive *atomic.Bool) bool {
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
		lv4.logger.Error("读取消息失败，关闭子连接", elog.String("linkID", l.id), elog.FieldErr(err))
		lv4.markLinkClosed(l, alive)
		return true // 错误也是一种活动
	}

	l.UpdateActiveTime()
	select {
	case lv4.unifiedReceiveCh <- Message{LinkID: l.id, Payload: payload}:
	default:
		lv4.logger.Warn("统一接收通道已满，消息被丢弃", elog.String("linkID", l.id))
	}
	return true
}

// tryWrite 尝试非阻塞写入
func (lv4 *LinkV4) tryWrite(l *Link, alive *atomic.Bool) bool {
	if !alive.Load() {
		return false
	}
	select {
	case payload, ok := <-l.sendCh:
		if !ok {
			lv4.markLinkClosed(l, alive)
			return true // 通道关闭是一种活动
		}

		// 执行单次非阻塞写，不带循环重试，以避免阻塞整个 readWriteLoop
		_ = l.conn.SetWriteDeadline(time.Now().Add(l.writeTimeout))
		_, err := l.writer.Write(payload)

		if err != nil {
			// 任何写入失败（包括超时）都将导致连接关闭。
			// 这是为了保证 readWriteLoop 的非阻塞性所做的必要权衡。
			lv4.logger.Error("写入消息失败，关闭子连接", elog.String("linkID", l.id), elog.FieldErr(err))
			lv4.markLinkClosed(l, alive)
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
func (lv4 *LinkV4) markLinkClosed(l *Link, alive *atomic.Bool) {
	if alive.CompareAndSwap(true, false) {
		_ = l.Close()
	}
}
