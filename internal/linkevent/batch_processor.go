package linkevent

import (
	"context"
	"errors"
	"fmt"
	"time"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/ecodeclub/ekit/retry"
	"github.com/ecodeclub/ekit/syncx"
	"github.com/gotomicro/ego/core/elog"
)

var ErrBatchResponseMismatch = errors.New("批量响应数量不匹配")

// BatchBackendClientLoader 批量后端客户端加载器
type BatchBackendClientLoader func() *syncx.Map[int64, apiv1.BatchBackendServiceClient]

// BatchProcessor 批量处理器，实现 batch.Processor 接口
// 职责：调用对应bizID的BatchBackendService.BatchOnReceive方法
type BatchProcessor struct {
	batchBackendClientLoader BatchBackendClientLoader
	bizToBatchClient         *syncx.Map[int64, apiv1.BatchBackendServiceClient]
	onReceiveTimeout         time.Duration
	initRetryInterval        time.Duration
	maxRetryInterval         time.Duration
	maxRetries               int32
	logger                   *elog.Component
}

// NewBatchProcessor 创建批量处理器
func NewBatchProcessor(
	batchBackendClientLoader BatchBackendClientLoader,
	onReceiveTimeout,
	initRetryInterval,
	maxRetryInterval time.Duration,
	maxRetries int32,
	logger *elog.Component,
) *BatchProcessor {
	return &BatchProcessor{
		batchBackendClientLoader: batchBackendClientLoader,
		bizToBatchClient:         &syncx.Map[int64, apiv1.BatchBackendServiceClient]{},
		onReceiveTimeout:         onReceiveTimeout,
		initRetryInterval:        initRetryInterval,
		maxRetryInterval:         maxRetryInterval,
		maxRetries:               maxRetries,
		logger:                   logger.With(elog.FieldComponent("BatchProcessor")),
	}
}

// Process 实现 batch.Processor 接口
// 批量处理上行消息，调用对应的BatchBackendService.BatchOnReceive方法
func (p *BatchProcessor) Process(bizID int64, reqs []*apiv1.OnReceiveRequest) ([]*apiv1.OnReceiveResponse, error) {
	// 获取批量后端服务客户端
	client, err := p.getBatchBackendServiceClient(bizID)
	if err != nil {
		return nil, err
	}

	// 构造批量请求
	batchReq := &apiv1.BatchOnReceiveRequest{
		Reqs: reqs,
	}

	p.logger.Info("开始批量处理请求",
		elog.Int64("bizID", bizID),
		elog.Int("requestCount", len(reqs)),
	)

	// 调用批量服务（带重试）
	batchResp, err := p.callBatchServiceWithRetry(client, batchReq)
	if err != nil {
		p.logger.Error("批量处理失败",
			elog.Int64("bizID", bizID),
			elog.Int("requestCount", len(reqs)),
			elog.FieldErr(err),
		)
		return nil, err
	}

	// 验证响应数量
	if len(batchResp.GetRes()) != len(reqs) {
		err = fmt.Errorf("%w: 期望%d个，实际%d个", ErrBatchResponseMismatch, len(reqs), len(batchResp.GetRes()))
		p.logger.Error("批量响应数量不匹配",
			elog.Int64("bizID", bizID),
			elog.Int("requestCount", len(reqs)),
			elog.Int("responseCount", len(batchResp.GetRes())),
			elog.FieldErr(err),
		)
		return nil, err
	}

	p.logger.Info("批量处理成功",
		elog.Int64("bizID", bizID),
		elog.Int("requestCount", len(reqs)),
		elog.Int("responseCount", len(batchResp.GetRes())),
	)

	return batchResp.GetRes(), nil
}

// getBatchBackendServiceClient 获取批量后端服务客户端
func (p *BatchProcessor) getBatchBackendServiceClient(bizID int64) (apiv1.BatchBackendServiceClient, error) {
	var loaded bool
	for {
		client, found := p.bizToBatchClient.Load(bizID)
		if !found {
			if !loaded {
				// 未找到且未重新加载过，重新加载批量后端GRPC客户端
				p.bizToBatchClient = p.batchBackendClientLoader()
				loaded = true
				continue
			}
			return nil, fmt.Errorf("%w: %d", ErrUnknownBizID, bizID)
		}
		return client, nil
	}
}

// callBatchServiceWithRetry 带重试的批量服务调用
func (p *BatchProcessor) callBatchServiceWithRetry(
	client apiv1.BatchBackendServiceClient,
	batchReq *apiv1.BatchOnReceiveRequest,
) (*apiv1.BatchOnReceiveResponse, error) {
	retryStrategy, _ := retry.NewExponentialBackoffRetryStrategy(
		p.initRetryInterval,
		p.maxRetryInterval,
		p.maxRetries,
	)

	for {
		ctx, cancelFunc := context.WithTimeout(context.Background(), p.onReceiveTimeout)
		resp, err := client.BatchOnReceive(ctx, batchReq)
		cancelFunc()

		if err == nil {
			return resp, nil
		}

		p.logger.Warn("调用批量后端服务失败",
			elog.String("step", "callBatchServiceWithRetry"),
			elog.FieldErr(err),
		)

		duration, ok := retryStrategy.Next()
		if !ok {
			return nil, fmt.Errorf("%w: %w", err, ErrMaxRetriesExceeded)
		}
		time.Sleep(duration)
	}
}
