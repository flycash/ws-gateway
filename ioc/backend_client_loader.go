package ioc

import (
	"context"
	"encoding/json"
	"log"
	"sync/atomic"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/pkg/grpc"
	"github.com/ecodeclub/ekit/syncx"
	"github.com/ego-component/eetcd"
	"github.com/ego-component/eetcd/registry"
	"github.com/gotomicro/ego/client/egrpc"
	"github.com/gotomicro/ego/core/econf"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type BackendServiceInfo struct {
	BizID int64 `json:"bizId"`
	// 服务发现用的服务名
	Name string `json:"name"`
}

type BackendClientsLoader struct {
	svcInfoPtr atomic.Pointer[[]BackendServiceInfo]
	etcdClient *eetcd.Component
}

func NewBackendClientLoader(services []BackendServiceInfo, etcdClient *eetcd.Component) *BackendClientsLoader {
	h := &BackendClientsLoader{
		etcdClient: etcdClient,
	}
	h.svcInfoPtr.Store(&services)
	return h
}

func (b *BackendClientsLoader) Load() *syncx.Map[int64, apiv1.BackendServiceClient] {
	// 遍历业务后端服务信息
	svcInfos := *b.svcInfoPtr.Load()

	grpcClients := grpc.NewClients(func(conn *egrpc.Component) apiv1.BackendServiceClient {
		return apiv1.NewBackendServiceClient(conn)
	})
	registry.Load("").Build(registry.WithClientEtcd(b.etcdClient))
	clients := &syncx.Map[int64, apiv1.BackendServiceClient]{}
	for i := range svcInfos {
		// 从注册中心获取业务后端的具体地址并创建相应的grpc客户端
		grpcClient := grpcClients.Get(svcInfos[i].Name)
		// 构建业务ID与业务后端GRPC客户端的映射关系
		clients.Store(svcInfos[i].BizID, grpcClient)
	}
	return clients
}

func (b *BackendClientsLoader) UpdateBackendServiceInfo(value []byte) error {
	var svcs []BackendServiceInfo
	err := json.Unmarshal(value, &svcs)
	if err == nil {
		log.Printf("UpdateBackendServiceInfo, svcs: %#v\n", svcs)
		// 更新业务后端服务信息
		b.svcInfoPtr.Store(&svcs)
	}
	return err
}

func InitBackendClientLoader(etcdClient *eetcd.Component) func() *syncx.Map[int64, apiv1.BackendServiceClient] {
	type Config struct {
		EtcdKey string `yaml:"etcdKey"`
	}
	var cfg Config
	err := econf.UnmarshalKey("backend.services", &cfg)
	if err != nil {
		panic(err)
	}

	backendClientLoader := NewBackendClientLoader([]BackendServiceInfo{}, etcdClient)

	// 处理业务后端服务信息变更事件
	go func() {
		watchChan := etcdClient.Watch(context.Background(), cfg.EtcdKey)
		for watchResp := range watchChan {
			for _, event := range watchResp.Events {
				if event.Type == clientv3.EventTypePut {
					// 更新业务后端服务信息
					_ = backendClientLoader.UpdateBackendServiceInfo(event.Kv.Value)
				}
			}
		}
	}()
	get, err := etcdClient.Get(context.Background(), cfg.EtcdKey)
	if err == nil {
		for i := range get.Kvs {
			_ = backendClientLoader.UpdateBackendServiceInfo(get.Kvs[i].Value)
		}
	}
	return backendClientLoader.Load
}
