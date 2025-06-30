package wsgrpc

import (
	"context"
	"sync/atomic"
	"time"

	gatewayapiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
)

const (
	sleepTime = 50 * time.Millisecond
	bizID     = 9999
)

type MockBackendServer struct {
	gatewayapiv1.UnimplementedBackendServiceServer
	msgID int64
}

func NewMockBackendServer() *MockBackendServer {
	return &MockBackendServer{}
}

func (m *MockBackendServer) OnReceive(_ context.Context, _ *gatewayapiv1.OnReceiveRequest) (*gatewayapiv1.OnReceiveResponse, error) {
	// 模拟处理
	time.Sleep(sleepTime)
	v := atomic.AddInt64(&m.msgID, 1)
	return &gatewayapiv1.OnReceiveResponse{
		MsgId: v,
		BizId: bizID,
	}, nil
}
