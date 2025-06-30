package batch

import (
	"errors"
	"fmt"
	"sync"
	"time"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/gotomicro/ego/core/elog"
)

var (
	ErrCoordinatorStopped    = errors.New("批处理协调器已停止")
	ErrBatchResponseMismatch = errors.New("批量响应数量与请求不匹配")
)

// Processor 批量处理器接口，由具体的Handler实现
// 职责：
// 1. 调用对应bizID的BatchBackendService.BatchOnReceive方法
// 2. 返回BatchOnReceiveResponse中的响应列表
//
// 参数：
// - bizID: 业务ID，用于选择对应的后端服务客户端
// - reqs: 批量请求列表，按顺序排列
//
// 返回值：
// - []*apiv1.OnReceiveResponse: 批量响应结果，顺序必须与请求一致
// - error: 处理过程中的错误(网络错误、服务错误等)
type Processor interface {
	Process(bizID int64, reqs []*apiv1.OnReceiveRequest) ([]*apiv1.OnReceiveResponse, error)
}

// Request 批处理请求，包含消息和结果通道
type Request struct {
	Message    *apiv1.OnReceiveRequest // 请求消息
	LinkID     string                  // WebSocket连接ID
	ResultChan chan *Result            // 结果通道
}

// Result 批量处理结果
type Result struct {
	Response *apiv1.OnReceiveResponse // 处理成功的响应
	Error    error                    // 处理失败的错误
}

// Batch 批次信息
type Batch struct {
	bizID    int64       // 业务ID
	mu       sync.Mutex  // 批次锁
	requests []*Request  // 请求列表
	timer    *time.Timer // 超时定时器
	closed   bool        // 是否已关闭
}

// Coordinator 批处理协调器
type Coordinator struct {
	batches      map[int64]*Batch // BizID -> Batch
	mu           sync.RWMutex     // 全局读写锁
	batchSize    int              // 批次大小阈值
	batchTimeout time.Duration    // 批次超时时间
	processor    Processor        // 批量处理器
	logger       *elog.Component  // 日志组件
	stopped      bool             // 是否已停止
	stopCh       chan struct{}    // 停止信号通道
}

// NewCoordinator 创建批处理协调器
func NewCoordinator(
	batchSize int,
	batchTimeout time.Duration,
	processor Processor,
	logger *elog.Component,
) *Coordinator {
	return &Coordinator{
		batches:      make(map[int64]*Batch),
		batchSize:    batchSize,
		batchTimeout: batchTimeout,
		processor:    processor,
		logger:       logger.With(elog.FieldComponent("BatchCoordinator")),
		stopCh:       make(chan struct{}),
	}
}

// OnReceive 添加请求到批次并等待处理结果
func (c *Coordinator) OnReceive(
	bizID int64,
	linkID string,
	req *apiv1.OnReceiveRequest,
) (*apiv1.OnReceiveResponse, error) {
	// 检查协调器是否已停止
	c.mu.RLock()
	if c.stopped {
		c.mu.RUnlock()
		return nil, ErrCoordinatorStopped
	}
	c.mu.RUnlock()

	// 创建请求
	request := &Request{
		Message:    req,
		LinkID:     linkID,
		ResultChan: make(chan *Result, 1),
	}

	// 将请求添加到批次，如果达到触发条件则立即处理
	c.addToBatch(bizID, request)

	// 等待处理结果
	select {
	case result := <-request.ResultChan:
		if result.Error != nil {
			return nil, result.Error
		}
		return result.Response, nil
	case <-c.stopCh:
		return nil, ErrCoordinatorStopped
	}
}

// addToBatch 将请求添加到批次，返回是否应该触发处理
// 集成了获取或创建批次的逻辑
func (c *Coordinator) addToBatch(bizID int64, request *Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped {
		// 协调器已停止，直接返回错误
		select {
		case request.ResultChan <- &Result{Error: ErrCoordinatorStopped}:
		default:
		}
		return
	}

	// 获取或创建批次
	batch, exists := c.batches[bizID]
	if !exists {
		batch = &Batch{
			bizID:    bizID,
			requests: make([]*Request, 0, c.batchSize),
		}

		// 设置超时定时器
		batch.timer = time.AfterFunc(c.batchTimeout, func() {
			c.logger.Debug("批次超时触发",
				elog.Int64("bizID", bizID),
				elog.Duration("timeout", c.batchTimeout),
			)
			c.triggerBatch(bizID)
		})

		c.batches[bizID] = batch
		c.logger.Debug("创建新批次",
			elog.Int64("bizID", bizID),
			elog.Duration("timeout", c.batchTimeout),
		)
	}

	// 将请求添加到批次
	batch.requests = append(batch.requests, request)

	c.logger.Debug("请求已添加到批次",
		elog.Int64("bizID", bizID),
		elog.String("requestKey", request.Message.GetKey()),
		elog.Int("currentSize", len(batch.requests)),
		elog.Int("threshold", c.batchSize),
	)

	// 检查是否达到批次大小阈值
	if len(batch.requests) >= c.batchSize {
		// 如果达到触发条件，立即处理批次
		go c.triggerBatch(bizID)
	}
}

// triggerBatch 触发批次处理
func (c *Coordinator) triggerBatch(bizID int64) {
	c.mu.Lock()
	batch, exists := c.batches[bizID]
	if !exists {
		c.mu.Unlock()
		return
	}

	// 从全局batches中移除
	delete(c.batches, bizID)
	c.mu.Unlock()

	// 锁定批次并处理
	batch.mu.Lock()
	defer batch.mu.Unlock()

	if batch.closed || len(batch.requests) == 0 {
		return
	}

	// 停止定时器
	if batch.timer != nil {
		batch.timer.Stop()
	}

	// 标记批次已关闭
	batch.closed = true

	c.logger.Info("开始处理批次",
		elog.Int64("bizID", bizID),
		elog.Int("requestCount", len(batch.requests)),
	)

	// 调用处理器处理批次
	// 从 Request 中提取业务请求数据
	reqs := make([]*apiv1.OnReceiveRequest, len(batch.requests))
	for i, req := range batch.requests {
		reqs[i] = req.Message
	}

	startTime := time.Now()
	responses, err := c.processor.Process(bizID, reqs)
	duration := time.Since(startTime)

	switch {
	case err != nil:
		c.logger.Error("批次处理失败",
			elog.Int64("bizID", bizID),
			elog.Int("requestCount", len(batch.requests)),
			elog.Duration("duration", duration),
			elog.FieldErr(err),
		)
		// 处理失败，向所有请求发送错误
		c.distributeError(batch.requests, err)

	case len(responses) != len(batch.requests):
		c.logger.Error(fmt.Sprintf("批量响应数量不匹配: 期望%d个，实际%d个", len(batch.requests), len(responses)),
			elog.Int64("bizID", bizID),
			elog.Int("requestCount", len(batch.requests)),
			elog.Int("responseCount", len(responses)),
		)
		// 响应数量不匹配，向所有请求发送错误
		c.distributeError(batch.requests, ErrBatchResponseMismatch)

	default:
		c.logger.Info("批次处理成功",
			elog.Int64("bizID", bizID),
			elog.Int("requestCount", len(batch.requests)),
			elog.Duration("duration", duration),
		)
		// 成功处理，按顺序分发结果
		c.distributeResults(batch.requests, responses)
	}
}

// distributeError 将错误分发给所有请求的ResultChan
func (c *Coordinator) distributeError(requests []*Request, err error) {
	for _, req := range requests {
		select {
		case req.ResultChan <- &Result{Error: err}:
		default:
			// ResultChan 已满或已关闭，记录警告但继续
			c.logger.Warn("无法发送错误结果：ResultChan已满",
				elog.String("requestKey", req.Message.GetKey()),
				elog.String("linkID", req.LinkID),
				elog.FieldErr(err),
			)
		}
	}
}

// distributeResults 将成功结果按顺序分发给各个请求的ResultChan
func (c *Coordinator) distributeResults(requests []*Request, responses []*apiv1.OnReceiveResponse) {
	for i, req := range requests {
		select {
		case req.ResultChan <- &Result{Response: responses[i]}:
			c.logger.Debug("已分发处理结果",
				elog.String("requestKey", req.Message.GetKey()),
				elog.String("linkID", req.LinkID),
			)
		default:
			// ResultChan 已满或已关闭，记录警告但继续
			c.logger.Warn("无法发送成功结果：ResultChan已满",
				elog.String("requestKey", req.Message.GetKey()),
				elog.String("linkID", req.LinkID),
			)
		}
	}
}

// Stop 停止协调器并优雅关闭：先停止接受新请求，处理完现有批次后再通知等待者
func (c *Coordinator) Stop() {
	// 第一阶段：标记停止，收集待处理的批次
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}

	c.stopped = true

	// 预分配切片空间，收集所有待处理的bizID
	pendingBizIDs := make([]int64, 0, len(c.batches))
	for bizID := range c.batches {
		pendingBizIDs = append(pendingBizIDs, bizID)
	}

	c.logger.Info("开始优雅停止批处理协调器",
		elog.Int("pendingBatches", len(pendingBizIDs)),
	)
	c.mu.Unlock()

	// 第二阶段：在不持有锁的情况下处理所有批次
	if len(pendingBizIDs) > 0 {
		// 使用WaitGroup等待所有批次处理完成
		var wg sync.WaitGroup
		for _, bizID := range pendingBizIDs {
			wg.Add(1)
			go func(id int64) {
				defer wg.Done()
				c.triggerBatch(id)
			}(bizID)
		}

		// 等待所有批次处理完成
		wg.Wait()
		c.logger.Info("所有待处理批次已完成")
	}

	// 第三阶段：关闭停止信号，通知所有等待者
	c.mu.Lock()
	close(c.stopCh)
	// 确保batches为空（triggerBatch应该已经清理了）
	c.batches = make(map[int64]*Batch)
	c.mu.Unlock()

	c.logger.Info("批处理协调器已优雅停止")
}

// GetStats 获取协调器统计信息（用于监控）
func (c *Coordinator) GetStats() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	stats := make(map[string]any)
	stats["batchCount"] = len(c.batches)
	stats["batchSize"] = c.batchSize
	stats["batchTimeout"] = c.batchTimeout.String()
	stats["stopped"] = c.stopped

	// 各个批次的详细信息
	batchDetails := make(map[int64]int)
	for bizID, batch := range c.batches {
		batch.mu.Lock()
		batchDetails[bizID] = len(batch.requests)
		batch.mu.Unlock()
	}
	stats["batchDetails"] = batchDetails

	return stats
}
