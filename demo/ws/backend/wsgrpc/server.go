package wsgrpc

import (
	"context"
	"encoding/json"
	"net"
	"time"

	msgv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/ego-component/eetcd"
	"github.com/ego-component/eetcd/registry"
	"github.com/gotomicro/ego/core/constant"
	"github.com/gotomicro/ego/core/elog"
	"github.com/gotomicro/ego/server"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
)

type Server struct {
	*grpc.Server
	name   string
	reg    *registry.Component
	etcd   *eetcd.Component
	addr   string
	bizID  int64
	logger *elog.Component
}

type BackendServiceInfo struct {
	BizID int64 `json:"bizId"`
	// 服务发现用的服务名
	Name string `json:"name"`
}

const (
	key             = "gateway.backend.services"
	serverSleepTime = 100 * time.Millisecond
	registerTime    = 10 * time.Second
)

func NewServer(name string,
	addr string,
	backendServer *MockBackendServer,
	reg *registry.Component,
	etcd *eetcd.Component,
) *Server {
	grpcServer := grpc.NewServer()
	msgv1.RegisterBackendServiceServer(grpcServer, backendServer)
	return &Server{
		Server: grpcServer,
		name:   name,
		reg:    reg,
		addr:   addr,
		bizID:  bizID,
		logger: elog.DefaultLogger,
		etcd:   etcd,
	}
}

func (s *Server) Run() error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	err = s.reg.RegisterService(context.Background(), &server.ServiceInfo{
		Name:    s.name,
		Address: s.addr,
		Scheme:  "grpc",
		Kind:    constant.ServiceProvider,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), registerTime)
	defer cancel()
	err = s.joinConfigPlatform(ctx)
	if err != nil {
		return err
	}
	s.logger.Info("grpc server started.......")
	return s.Server.Serve(listener)
}

// 加入配置中心
func (s *Server) joinConfigPlatform(ctx context.Context) error {
	// 获取当前服务信息
	serviceInfo := BackendServiceInfo{
		BizID: s.bizID,
		Name:  s.name,
	}

	for {
		// 获取当前版本和值
		getResp, err := s.etcd.Get(ctx, key)
		if err != nil {
			return err
		}

		var services []BackendServiceInfo
		var version int64

		// 如果键不存在，创建新的服务列表
		if len(getResp.Kvs) == 0 {
			services = []BackendServiceInfo{serviceInfo}
			version = 0
		} else {
			// 解析现有的服务列表
			if err := json.Unmarshal(getResp.Kvs[0].Value, &services); err != nil {
				return err
			}
			version = getResp.Kvs[0].Version

			// 检查服务是否已存在
			exists := false
			for _, svc := range services {
				if svc.BizID == serviceInfo.BizID && svc.Name == serviceInfo.Name {
					exists = true
					break
				}
			}

			// 如果服务不存在，添加到列表
			if !exists {
				services = append(services, serviceInfo)
			}
		}

		// 序列化新的服务列表
		newValue, err := json.Marshal(services)
		if err != nil {
			return err
		}

		// 执行原子操作
		txnResp, err := s.etcd.Txn(ctx).
			If(clientv3.Compare(clientv3.Version(key), "=", version)).
			Then(clientv3.OpPut(key, string(newValue))).
			Commit()
		if err != nil {
			return err
		}

		// 如果原子操作成功，退出循环
		if txnResp.Succeeded {
			return nil
		}

		time.Sleep(serverSleepTime)
	}
}
