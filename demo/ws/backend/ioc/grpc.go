package ioc

import (
	"gitee.com/flycash/ws-gateway/demo/ws/backend/wsgrpc"
	"github.com/gotomicro/ego/core/econf"
)

func InitGrpcServer() *wsgrpc.Server {
	mockServer := wsgrpc.NewMockBackendServer()
	etcdClient := InitEtcdClient()
	reg := InitRegistry()
	type cfg struct {
		Name string `yaml:"name"`
		Addr string `yaml:"addr"`
	}
	var conf cfg
	err := econf.UnmarshalKey("server.grpc", &conf)
	if err != nil {
		panic(err)
	}
	return wsgrpc.NewServer(conf.Name, conf.Addr, mockServer, reg, etcdClient)
}
