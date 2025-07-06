//go:build unit

package linkevent_test

import (
	"errors"
	"gitee.com/flycash/ws-gateway/internal/linkevent"
	"testing"
	"time"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/gotomicro/ego/core/elog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockProcessor 模拟批量处理器
type MockProcessor struct {
	mock.Mock
}

func (m *MockProcessor) Process(bizID int64, reqs []*apiv1.OnReceiveRequest) ([]*apiv1.OnReceiveResponse, error) {
	args := m.Called(bizID, reqs)

	// 从mock期望中获取返回值
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	return args.Get(0).([]*apiv1.OnReceiveResponse), args.Error(1)
}

func TestCoordinator_AddRequest_BatchSizeThreshold(t *testing.T) {
	// 准备
	logger := elog.DefaultLogger
	processor := &MockProcessor{}
	coordinator := linkevent.NewCoordinator(2, time.Second, processor, logger)

	bizID := int64(123)
	linkID1 := "link1"
	linkID2 := "link2"

	req1 := &apiv1.OnReceiveRequest{
		Key:  "key1",
		Body: []byte("message1"),
	}

	req2 := &apiv1.OnReceiveRequest{
		Key:  "key2",
		Body: []byte("message2"),
	}

	// 设置期望
	processor.On("Process", bizID, mock.MatchedBy(func(reqs []*apiv1.OnReceiveRequest) bool {
		return len(reqs) == 2
	})).Return([]*apiv1.OnReceiveResponse{
		{MsgId: 1, BizId: bizID},
		{MsgId: 2, BizId: bizID},
	}, nil)

	// 执行 - 并发发送两个请求
	resultCh1 := make(chan *apiv1.OnReceiveResponse, 1)
	resultCh2 := make(chan *apiv1.OnReceiveResponse, 1)
	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)

	go func() {
		resp, err := coordinator.OnReceive(bizID, linkID1, req1)
		resultCh1 <- resp
		errCh1 <- err
	}()

	go func() {
		resp, err := coordinator.OnReceive(bizID, linkID2, req2)
		resultCh2 <- resp
		errCh2 <- err
	}()

	// 验证
	select {
	case resp1 := <-resultCh1:
		assert.NoError(t, <-errCh1)
		assert.Equal(t, bizID, resp1.BizId)
	case <-time.After(time.Second):
		t.Fatal("请求1处理超时")
	}

	select {
	case resp2 := <-resultCh2:
		assert.NoError(t, <-errCh2)
		assert.Equal(t, bizID, resp2.BizId)
	case <-time.After(time.Second):
		t.Fatal("请求2处理超时")
	}

	processor.AssertExpectations(t)
}

func TestCoordinator_AddRequest_TimeoutThreshold(t *testing.T) {
	// 准备
	logger := elog.DefaultLogger
	processor := &MockProcessor{}
	coordinator := linkevent.NewCoordinator(10, 100*time.Millisecond, processor, logger) // 100ms超时

	bizID := int64(456)
	linkID := "link1"

	req := &apiv1.OnReceiveRequest{
		Key:  "key1",
		Body: []byte("message1"),
	}

	// 设置期望 - 应该因为超时触发批处理
	processor.On("Process", bizID, mock.MatchedBy(func(reqs []*apiv1.OnReceiveRequest) bool {
		return len(reqs) == 1 // 只有一个请求
	})).Return([]*apiv1.OnReceiveResponse{{MsgId: 1, BizId: bizID}}, nil)

	// 执行
	start := time.Now()
	resp, err := coordinator.OnReceive(bizID, linkID, req)
	duration := time.Since(start)

	// 验证
	assert.NoError(t, err)
	assert.Equal(t, bizID, resp.BizId)
	assert.GreaterOrEqual(t, duration, 100*time.Millisecond) // 至少等待了超时时间
	assert.LessOrEqual(t, duration, 200*time.Millisecond)    // 但不会太久

	processor.AssertExpectations(t)
}

func TestCoordinator_Stop(t *testing.T) {
	// 准备
	logger := elog.DefaultLogger
	processor := &MockProcessor{}
	coordinator := linkevent.NewCoordinator(10, time.Second, processor, logger)

	// 执行
	coordinator.Stop()

	// 验证 - 停止后不能添加新请求
	bizID := int64(789)
	linkID := "link1"
	req := &apiv1.OnReceiveRequest{
		Key:  "key1",
		Body: []byte("message1"),
	}

	resp, err := coordinator.OnReceive(bizID, linkID, req)
	assert.Error(t, err)
	assert.Equal(t, linkevent.ErrCoordinatorStopped, err)
	assert.Nil(t, resp)
}

func TestCoordinator_GracefulStop(t *testing.T) {
	// 准备
	logger := elog.DefaultLogger
	processor := &MockProcessor{}
	coordinator := linkevent.NewCoordinator(10, 500*time.Millisecond, processor, logger) // 500ms超时

	bizID := int64(123)
	linkID := "link1"
	req := &apiv1.OnReceiveRequest{
		Key:  "key1",
		Body: []byte("message1"),
	}

	// 设置期望 - 处理器会被调用
	processor.On("Process", bizID, mock.MatchedBy(func(reqs []*apiv1.OnReceiveRequest) bool {
		return len(reqs) == 1
	})).Return([]*apiv1.OnReceiveResponse{{MsgId: 1, BizId: bizID}}, nil)

	// 先添加一个请求（但不足以触发批次大小）
	resultCh := make(chan *apiv1.OnReceiveResponse, 1)
	errCh := make(chan error, 1)

	go func() {
		resp, err := coordinator.OnReceive(bizID, linkID, req)
		resultCh <- resp
		errCh <- err
	}()

	// 等待一小段时间确保请求已添加到批次
	time.Sleep(50 * time.Millisecond)

	// 执行优雅停止
	start := time.Now()
	coordinator.Stop()
	stopDuration := time.Since(start)

	// 验证 - 请求应该被处理完成
	select {
	case resp := <-resultCh:
		assert.NoError(t, <-errCh)
		assert.Equal(t, bizID, resp.BizId)
	case <-time.After(time.Second):
		t.Fatal("优雅停止后请求没有被处理")
	}

	// 验证停止时间合理（应该立即处理，不等超时）
	assert.Less(t, stopDuration, 200*time.Millisecond)

	processor.AssertExpectations(t)
}

func TestCoordinator_StopWithMultipleBatches(t *testing.T) {
	// 准备
	logger := elog.DefaultLogger
	processor := &MockProcessor{}
	coordinator := linkevent.NewCoordinator(10, time.Second, processor, logger)

	// 设置期望 - 两个bizID的批次都会被处理
	processor.On("Process", int64(123), mock.MatchedBy(func(reqs []*apiv1.OnReceiveRequest) bool {
		return len(reqs) == 1
	})).Return([]*apiv1.OnReceiveResponse{{MsgId: 1, BizId: 123}}, nil)
	processor.On("Process", int64(456), mock.MatchedBy(func(reqs []*apiv1.OnReceiveRequest) bool {
		return len(reqs) == 1
	})).Return([]*apiv1.OnReceiveResponse{{MsgId: 2, BizId: 456}}, nil)

	// 添加多个不同bizID的请求
	req1 := &apiv1.OnReceiveRequest{Key: "key1", Body: []byte("message1")}
	req2 := &apiv1.OnReceiveRequest{Key: "key2", Body: []byte("message2")}

	resultCh1 := make(chan *apiv1.OnReceiveResponse, 1)
	resultCh2 := make(chan *apiv1.OnReceiveResponse, 1)
	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)

	go func() {
		resp, err := coordinator.OnReceive(123, "link1", req1)
		resultCh1 <- resp
		errCh1 <- err
	}()

	go func() {
		resp, err := coordinator.OnReceive(456, "link2", req2)
		resultCh2 <- resp
		errCh2 <- err
	}()

	// 等待请求添加到批次
	time.Sleep(50 * time.Millisecond)

	// 执行优雅停止
	coordinator.Stop()

	// 验证所有请求都被处理
	select {
	case resp1 := <-resultCh1:
		assert.NoError(t, <-errCh1)
		assert.Equal(t, int64(123), resp1.BizId)
	case <-time.After(time.Second):
		t.Fatal("bizID 123的请求没有被处理")
	}

	select {
	case resp2 := <-resultCh2:
		assert.NoError(t, <-errCh2)
		assert.Equal(t, int64(456), resp2.BizId)
	case <-time.After(time.Second):
		t.Fatal("bizID 456的请求没有被处理")
	}

	processor.AssertExpectations(t)
}

func TestCoordinator_ProcessorError(t *testing.T) {
	// 准备
	logger := elog.DefaultLogger
	processor := &MockProcessor{}
	coordinator := linkevent.NewCoordinator(2, time.Second, processor, logger)

	bizID := int64(123)
	processingError := errors.New("处理器错误")

	// 设置期望 - 处理器返回错误
	processor.On("Process", bizID, mock.MatchedBy(func(reqs []*apiv1.OnReceiveRequest) bool {
		return len(reqs) == 2
	})).Return(([]*apiv1.OnReceiveResponse)(nil), processingError)

	req1 := &apiv1.OnReceiveRequest{Key: "key1", Body: []byte("message1")}
	req2 := &apiv1.OnReceiveRequest{Key: "key2", Body: []byte("message2")}

	// 并发发送两个请求触发批次处理
	resultCh1 := make(chan *apiv1.OnReceiveResponse, 1)
	resultCh2 := make(chan *apiv1.OnReceiveResponse, 1)
	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)

	go func() {
		resp, err := coordinator.OnReceive(bizID, "link1", req1)
		resultCh1 <- resp
		errCh1 <- err
	}()

	go func() {
		resp, err := coordinator.OnReceive(bizID, "link2", req2)
		resultCh2 <- resp
		errCh2 <- err
	}()

	// 验证两个请求都收到错误
	select {
	case <-resultCh1:
		err1 := <-errCh1
		assert.Error(t, err1)
		assert.Equal(t, processingError, err1)
	case <-time.After(time.Second):
		t.Fatal("请求1没有收到错误响应")
	}

	select {
	case <-resultCh2:
		err2 := <-errCh2
		assert.Error(t, err2)
		assert.Equal(t, processingError, err2)
	case <-time.After(time.Second):
		t.Fatal("请求2没有收到错误响应")
	}

	processor.AssertExpectations(t)
}

func TestCoordinator_ResponseMismatch(t *testing.T) {
	// 准备
	logger := elog.DefaultLogger
	processor := &MockProcessor{}
	coordinator := linkevent.NewCoordinator(2, time.Second, processor, logger)

	bizID := int64(123)

	// 设置期望 - 处理器返回的响应数量不匹配
	processor.On("Process", bizID, mock.MatchedBy(func(reqs []*apiv1.OnReceiveRequest) bool {
		return len(reqs) == 2
	})).Return([]*apiv1.OnReceiveResponse{
		{MsgId: 1, BizId: bizID}, // 只返回1个响应，但期望2个
	}, nil)

	req1 := &apiv1.OnReceiveRequest{Key: "key1", Body: []byte("message1")}
	req2 := &apiv1.OnReceiveRequest{Key: "key2", Body: []byte("message2")}

	// 并发发送两个请求触发批次处理
	resultCh1 := make(chan *apiv1.OnReceiveResponse, 1)
	resultCh2 := make(chan *apiv1.OnReceiveResponse, 1)
	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)

	go func() {
		resp, err := coordinator.OnReceive(bizID, "link1", req1)
		resultCh1 <- resp
		errCh1 <- err
	}()

	go func() {
		resp, err := coordinator.OnReceive(bizID, "link2", req2)
		resultCh2 <- resp
		errCh2 <- err
	}()

	// 验证两个请求都收到不匹配错误
	select {
	case <-resultCh1:
		err1 := <-errCh1
		assert.Error(t, err1)
		assert.Equal(t, linkevent.ErrBatchResponseMismatch, err1)
	case <-time.After(time.Second):
		t.Fatal("请求1没有收到错误响应")
	}

	select {
	case <-resultCh2:
		err2 := <-errCh2
		assert.Error(t, err2)
		assert.Equal(t, linkevent.ErrBatchResponseMismatch, err2)
	case <-time.After(time.Second):
		t.Fatal("请求2没有收到错误响应")
	}

	processor.AssertExpectations(t)
}

func TestCoordinator_GetStats(t *testing.T) {
	// 准备
	logger := elog.DefaultLogger
	processor := &MockProcessor{}
	coordinator := linkevent.NewCoordinator(5, time.Minute, processor, logger)

	// 执行
	stats := coordinator.GetStats()

	// 验证
	assert.Equal(t, 0, stats["batchCount"])
	assert.Equal(t, 5, stats["batchSize"])
	assert.Equal(t, time.Minute.String(), stats["batchTimeout"])
	assert.Equal(t, false, stats["stopped"])
	assert.NotNil(t, stats["batchDetails"])
}
