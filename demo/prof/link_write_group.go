package prof

import (
	"context"
	"errors"
	"net"

	"github.com/ecodeclub/ekit/pool"
	"github.com/ecodeclub/ekit/syncx"
	"github.com/gotomicro/ego/core/elog"
)

var ErrLinkGroupClosed = errors.New("连接组已关闭")

type Link struct {
	id string

	// 连接和配置
	conn net.Conn
}

// NewLink 创建一个使用 net.Conn 的简易 Link，用于测试或自定义场景。
func NewLink(id string, conn net.Conn) *Link {
	lk := &Link{
		id:   id,
		conn: conn,
	}
	return lk
}

func (c *Link) ID() string {
	return c.id
}

func (c *Link) Read(p []byte) (n int, err error) {
	return c.conn.Read(p)
}

func (c *Link) Write(p []byte) (n int, err error) {
	return c.conn.Write(p)
}

type writeRequest struct {
	id      string
	payload []byte
}

// PartLinkWriteGroup 代表"部分 Link" 共享一个写协程的写组实现。
// 典型场景：当网关实例拥有海量连接时，可以将它们按某种 hash/sharding 规则
// 拆分到多个 PartLinkWriteGroup，每个组仅启动一个 writeLoop 来减少 goroutine 数量。
type PartLinkWriteGroup struct {
	links  syncx.Map[string, *Link] // linkID -> *Link
	dataCh chan *writeRequest

	ctx    context.Context
	cancel context.CancelFunc

	logger *elog.Component
}

// NewPartLinkWriteGroup 创建 PartLinkWriteGroup。
//
//	buffer 表示 dataCh 的容量；当为 0 时创建无缓冲通道。
func NewPartLinkWriteGroup(parent context.Context, buffer int) *PartLinkWriteGroup {
	ctx, cancel := context.WithCancel(parent)
	lg := &PartLinkWriteGroup{
		dataCh: make(chan *writeRequest, buffer),
		ctx:    ctx,
		cancel: cancel,
		logger: elog.EgoLogger.With(elog.FieldComponent("PartLinkWriteGroup")),
	}
	go lg.writeLoop()
	return lg
}

func (lg *PartLinkWriteGroup) Add(l *Link) {
	lg.links.Store(l.id, l)
}

func (lg *PartLinkWriteGroup) Del(l *Link) {
	lg.links.Delete(l.id)
}

func (lg *PartLinkWriteGroup) Close() {
	lg.cancel()
}

func (lg *PartLinkWriteGroup) Write(id string, p []byte) error {
	if lg.ctx.Err() != nil {
		return ErrLinkGroupClosed
	}
	select {
	case lg.dataCh <- &writeRequest{id: id, payload: p}:
		return nil
	case <-lg.ctx.Done():
		return ErrLinkGroupClosed
	}
}

func (lg *PartLinkWriteGroup) writeLoop() {
	for {
		select {
		case <-lg.ctx.Done():
			return
		case d := <-lg.dataCh:
			lk, ok := lg.links.Load(d.id)
			if !ok {
				lg.logger.Warn("未知的 linkID", elog.String("linkID", d.id))
				continue
			}
			if _, err := lk.Write(d.payload); err != nil {
				lg.logger.Warn("写数据失败", elog.String("linkID", d.id), elog.FieldErr(err))
			}
		}
	}
}

// AllLinkWriteGroup 代表所有 Link 共用一个固定大小任务池的写协程实现。
// 可以通过限制任务池大小来对并发度进行更精细的控制。
type AllLinkWriteGroup struct {
	links  syncx.Map[string, *Link]
	dataCh chan *writeRequest

	ctx    context.Context
	cancel context.CancelFunc

	taskPool pool.TaskPool

	logger *elog.Component
}

// NewAllLinkWriteGroup 创建 AllLinkWriteGroup。
//
//	buffer: dataCh 容量；worker: 任务池协程数量。
func NewAllLinkWriteGroup(parent context.Context, buffer int, taskPool pool.TaskPool) (*AllLinkWriteGroup, error) {
	if taskPool == nil {
		return nil, errors.New("taskPool 不能为空")
	}
	ctx, cancel := context.WithCancel(parent)
	lg := &AllLinkWriteGroup{
		dataCh:   make(chan *writeRequest, buffer),
		ctx:      ctx,
		cancel:   cancel,
		taskPool: taskPool,
		logger:   elog.EgoLogger.With(elog.FieldComponent("AllLinkWriteGroup")),
	}
	go lg.writeLoop(ctx)
	return lg, nil
}

func (lg *AllLinkWriteGroup) Add(l *Link) {
	lg.links.Store(l.id, l)
}

func (lg *AllLinkWriteGroup) Del(l *Link) {
	lg.links.Delete(l.id)
}

func (lg *AllLinkWriteGroup) Close() {
	lg.cancel()
}

func (lg *AllLinkWriteGroup) Write(id string, p []byte) error {
	if lg.ctx.Err() != nil {
		return ErrLinkGroupClosed
	}
	select {
	case lg.dataCh <- &writeRequest{id: id, payload: p}:
		return nil
	case <-lg.ctx.Done():
		return ErrLinkGroupClosed
	}
}

func (lg *AllLinkWriteGroup) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case d := <-lg.dataCh:
			lk, ok := lg.links.Load(d.id)
			if !ok {
				lg.logger.Warn("未知的 linkID", elog.String("linkID", d.id))
				continue
			}
			// 使用协程任务池，控制并发
			err := lg.taskPool.Submit(ctx, pool.TaskFunc(func(_ context.Context) error {
				if _, err := lk.Write(d.payload); err != nil {
					lg.logger.Warn("写数据失败", elog.String("linkID", d.id), elog.FieldErr(err))
					return err
				}
				return nil
			}))
			if err != nil {
				lg.logger.Warn("提交任务到协程任务池失败", elog.FieldErr(err))
			}
		}
	}
}
