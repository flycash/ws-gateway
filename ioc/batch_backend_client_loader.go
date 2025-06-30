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

// BatchBackendClientsLoader 批量后端客户端加载器
type BatchBackendClientsLoader struct {
	svcInfoPtr atomic.Pointer[[]BackendServiceInfo]
	etcdClient *eetcd.Component
}

// NewBatchBackendClientLoader 创建批量后端客户端加载器
func NewBatchBackendClientLoader(services []BackendServiceInfo, etcdClient *eetcd.Component) *BatchBackendClientsLoader {
	h := &BatchBackendClientsLoader{
		etcdClient: etcdClient,
	}
	h.svcInfoPtr.Store(&services)
	return h
}

// Load 加载批量后端服务客户端映射
func (b *BatchBackendClientsLoader) Load() *syncx.Map[int64, apiv1.BatchBackendServiceClient] {
	// 遍历业务后端服务信息
	svcInfos := *b.svcInfoPtr.Load()

	// 创建批量后端服务客户端的 grpc 客户端池
	grpcClients := grpc.NewClients(func(conn *egrpc.Component) apiv1.BatchBackendServiceClient {
		return apiv1.NewBatchBackendServiceClient(conn)
	})
	registry.Load("").Build(registry.WithClientEtcd(b.etcdClient))

	clients := &syncx.Map[int64, apiv1.BatchBackendServiceClient]{}
	for i := range svcInfos {
		// 从注册中心获取业务后端的具体地址并创建相应的批量grpc客户端
		grpcClient := grpcClients.Get(svcInfos[i].Name)
		// 构建业务ID与业务后端批量GRPC客户端的映射关系
		clients.Store(svcInfos[i].BizID, grpcClient)
	}
	return clients
}

// UpdateBackendServiceInfo 更新后端服务信息
func (b *BatchBackendClientsLoader) UpdateBackendServiceInfo(value []byte) error {
	var svcs []BackendServiceInfo
	err := json.Unmarshal(value, &svcs)
	if err == nil {
		log.Printf("UpdateBatchBackendServiceInfo, svcs: %#v\n", svcs)
		// 更新业务后端服务信息
		b.svcInfoPtr.Store(&svcs)
	}
	return err
}

// InitBatchBackendClientLoader 初始化批量后端客户端加载器
//
//nolint:dupl // 忽略
func InitBatchBackendClientLoader(etcdClient *eetcd.Component) func() *syncx.Map[int64, apiv1.BatchBackendServiceClient] {
	type Config struct {
		EtcdKey string `yaml:"etcdKey"`
	}
	var cfg Config
	err := econf.UnmarshalKey("backend.services", &cfg)
	if err != nil {
		panic(err)
	}

	batchBackendClientLoader := NewBatchBackendClientLoader([]BackendServiceInfo{}, etcdClient)

	// 处理业务后端服务信息变更事件
	go func() {
		watchChan := etcdClient.Watch(context.Background(), cfg.EtcdKey)
		for watchResp := range watchChan {
			for _, event := range watchResp.Events {
				if event.Type == clientv3.EventTypePut {
					// 更新批量业务后端服务信息
					_ = batchBackendClientLoader.UpdateBackendServiceInfo(event.Kv.Value)
				}
			}
		}
	}()

	get, err := etcdClient.Get(context.Background(), cfg.EtcdKey)
	if err == nil {
		for i := range get.Kvs {
			_ = batchBackendClientLoader.UpdateBackendServiceInfo(get.Kvs[i].Value)
		}
	}
	return batchBackendClientLoader.Load
}
