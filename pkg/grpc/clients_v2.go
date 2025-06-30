package grpc

import (
	"fmt"
	"time"

	"gitee.com/flycash/ws-gateway/pkg/grpc/registry"
	"github.com/ecodeclub/ekit/syncx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/credentials/insecure"
)

type ClientsV2[T any] struct {
	clientMap syncx.Map[string, T]
	registry  registry.Registry
	timeout   time.Duration
	balancer  balancer.Builder
	creator   func(conn *grpc.ClientConn) T
}

func NewClientsV2[T any](
	registry registry.Registry,
	timeout time.Duration,
	balancer balancer.Builder,
	creator func(conn *grpc.ClientConn) T,
) *ClientsV2[T] {
	return &ClientsV2[T]{
		registry: registry,
		timeout:  timeout,
		balancer: balancer,
		creator:  creator,
	}
}

// Get 获取带有自定义负载均衡器的客户端
func (c *ClientsV2[T]) Get(serviceName string) T {
	client, ok := c.clientMap.Load(serviceName)
	if !ok {
		// 构建带有自定义负载均衡器的连接，如果服务发现失败，会 panic
		grpcConn, err := grpc.NewClient(
			fmt.Sprintf("backend:///%s", serviceName),
			// 注入解析器
			grpc.WithResolvers(NewResolverBuilder(c.registry, c.timeout)),
			// 默认负载均衡器实现
			grpc.WithDefaultServiceConfig(fmt.Sprintf(`{"loadBalancingPolicy":%q}`, c.balancer.Name())),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			panic(err)
		}
		client = c.creator(grpcConn)
		c.clientMap.Store(serviceName, client)
	}
	return client
}
