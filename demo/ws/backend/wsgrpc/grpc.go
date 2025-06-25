package wsgrpc

import (
	"context"
	"fmt"
	"gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"sync/atomic"
	"time"
)

type MockBackendServer struct {
	gatewayapiv1.UnimplementedBackendServiceServer
	msgID int64
}

func NewMockBackendServer() *MockBackendServer {
	return &MockBackendServer{}
}

func (m *MockBackendServer) OnReceive(ctx context.Context, request *gatewayapiv1.OnReceiveRequest) (*gatewayapiv1.OnReceiveResponse, error) {
	// 模拟处理
	time.Sleep(50 * time.Millisecond)
	v := atomic.AddInt64(&m.msgID, 1)
	fmt.Println(v)
	return &gatewayapiv1.OnReceiveResponse{
		MsgId: v,
		BizId: 9999,
	}, nil
}
