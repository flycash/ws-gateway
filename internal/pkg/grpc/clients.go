package grpc

import (
	"fmt"

	"github.com/ecodeclub/ekit/syncx"
	"github.com/gotomicro/ego/client/egrpc"
)

type Clients[T any] struct {
	clientMap syncx.Map[string, T]
	creator   func(conn *egrpc.Component) T
}

func NewClients[T any](creator func(conn *egrpc.Component) T) *Clients[T] {
	return &Clients[T]{creator: creator}
}

func (c *Clients[T]) Get(serviceName string) T {
	client, ok := c.clientMap.Load(serviceName)
	if !ok {
		// 我要初始化 client
		// ego 如果服务发现失败，会 panic
		grpcConn := egrpc.Load("").Build(egrpc.WithAddr(fmt.Sprintf("etcd:///%s", serviceName)))
		client = c.creator(grpcConn)
		c.clientMap.Store(serviceName, client)
	}
	return client
}
