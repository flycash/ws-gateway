//go:build wireinject

package ioc

import (
	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"gitee.com/flycash/ws-gateway/pkg/jwt"

	"gitee.com/flycash/ws-gateway/ioc"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/ego-component/eetcd"
	"github.com/google/wire"
	"github.com/gotomicro/ego/core/econf"
)

type App struct {
	OrderServer []gateway.Server
}

func InitApp() App {
	wire.Build(
		ioc.InitLocalCache,
		ioc.InitUserToken,
		ioc.InitEtcdClient,
		ioc.InitMQ,
		convertToWebsocketComponents,

		wire.Struct(new(App), "*"))
	return App{}
}

func convertToWebsocketComponents(
	messageQueue mq.MQ,
	localCache ecache.Cache,
	userToken *jwt.UserToken,
	etcdClient *eetcd.Component,
) []gateway.Server {
	configKey := "server.websocket"
	config := econf.GetStringMap(configKey)
	serializer, ok := config["serializer"].(string)
	if !ok {
		panic("server.websocket.serializer配置解析错误")
	}
	delete(config, "serializer")

	codecMapping := map[string]codec.Codec{
		"json":  codec.NewIgnoreAnyProtoFieldJSONCodec(),
		"proto": codec.NewProtoCodec(),
	}

	s := make([]gateway.Server, 0, len(config))
	// for i := range config {
	// configKey := fmt.Sprintf("server.websocket.%d", i)
	s = append(s, ioc.InitWebSocketServer(configKey, messageQueue, localCache, userToken, codecMapping[serializer], etcdClient))
	// }
	return s
}
