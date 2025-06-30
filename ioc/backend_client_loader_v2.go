package ioc

import (
	"context"
	"encoding/json"
	"log"
	"sync/atomic"
	"time"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	grpcpkg "gitee.com/flycash/ws-gateway/pkg/grpc"
	capacityBalancerV2 "gitee.com/flycash/ws-gateway/pkg/grpc/balancer/capacity/v2"
	registry "gitee.com/flycash/ws-gateway/pkg/grpc/registry/etcd"
	"github.com/ecodeclub/ekit/syncx"
	"github.com/ego-component/eetcd"
	"github.com/gotomicro/ego/core/econf"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
)

type BackendClientsLoaderV2 struct {
	svcInfoPtr atomic.Pointer[[]BackendServiceInfo]
	registry   *registry.Registry
	timeout    time.Duration
}

func NewBackendClientLoaderV2(services []BackendServiceInfo, registry *registry.Registry, timeout time.Duration) *BackendClientsLoaderV2 {
	h := &BackendClientsLoaderV2{
		registry: registry,
		timeout:  timeout,
	}
	h.svcInfoPtr.Store(&services)
	return h
}

func (b *BackendClientsLoaderV2) Load() *syncx.Map[int64, apiv1.BackendServiceClient] {
	// 遍历业务后端服务信息
	svcInfos := *b.svcInfoPtr.Load()

	grpcClients := grpcpkg.NewClientsV2(
		b.registry,
		b.timeout,
		capacityBalancerV2.NewBuilder(),
		func(conn *grpc.ClientConn) apiv1.BackendServiceClient {
			return apiv1.NewBackendServiceClient(conn)
		})

	clients := &syncx.Map[int64, apiv1.BackendServiceClient]{}
	for i := range svcInfos {
		// 从注册中心获取业务后端的具体地址并创建相应的grpc客户端
		grpcClient := grpcClients.Get(svcInfos[i].Name)
		// 构建业务ID与业务后端GRPC客户端的映射关系
		clients.Store(svcInfos[i].BizID, grpcClient)
	}
	return clients
}

func (b *BackendClientsLoaderV2) UpdateBackendServiceInfo(value []byte) error {
	var svcs []BackendServiceInfo
	err := json.Unmarshal(value, &svcs)
	if err == nil {
		log.Printf("UpdateBackendServiceInfo, svcs: %#v\n", svcs)
		// 更新业务后端服务信息
		b.svcInfoPtr.Store(&svcs)
	}
	return err
}

func InitBackendClientLoaderV2(etcdClient *eetcd.Component) func() *syncx.Map[int64, apiv1.BackendServiceClient] {
	type Config struct {
		EtcdKey string `yaml:"etcdKey"`
	}
	var cfg Config
	err := econf.UnmarshalKey("backend.services", &cfg)
	if err != nil {
		panic(err)
	}

	r, err := registry.NewRegistry(etcdClient)
	if err != nil {
		panic(err)
	}

	const defaultTimeout = 3 * time.Second
	backendClientLoaderV2 := NewBackendClientLoaderV2([]BackendServiceInfo{}, r, defaultTimeout)

	// 处理业务后端服务信息变更事件
	go func() {
		watchChan := etcdClient.Watch(context.Background(), cfg.EtcdKey)
		for watchResp := range watchChan {
			for _, event := range watchResp.Events {
				if event.Type == clientv3.EventTypePut {
					// 更新业务后端服务信息
					_ = backendClientLoaderV2.UpdateBackendServiceInfo(event.Kv.Value)
				}
			}
		}
	}()
	get, err := etcdClient.Get(context.Background(), cfg.EtcdKey)
	if err == nil {
		for i := range get.Kvs {
			_ = backendClientLoaderV2.UpdateBackendServiceInfo(get.Kvs[i].Value)
		}
	}
	return backendClientLoaderV2.Load
}
