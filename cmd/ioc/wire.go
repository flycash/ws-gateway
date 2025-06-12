//go:build wireinject

package ioc

import (
	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/pkg/jwt"

	"gitee.com/flycash/ws-gateway/ioc"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/google/wire"
)

type App struct {
	OrderServer []gateway.Server
}

func InitApp() App {
	wire.Build(
		ioc.InitRedisCmd,
		ioc.InitRedisCache,
		ioc.InitUserToken,
		ioc.InitEtcdClient,
		ioc.InitMQ,
		ioc.InitSerializer,

		ioc.InitLinkEventHandlerWrapper,

		convertToWebsocketComponents,

		wire.Struct(new(App), "*"))
	return App{}
}

func convertToWebsocketComponents(
	messageQueue mq.MQ,
	c ecache.Cache,
	userToken *jwt.UserToken,
	wrapper *gateway.LinkEventHandlerWrapper,
) []gateway.Server {
	configKey := "server.websocket"
	s := make([]gateway.Server, 0, 1)
	// for i := range config {
	// configKey := fmt.Sprintf("server.websocket.%d", i)
	s = append(s, ioc.InitWebSocketServer(configKey, messageQueue, c, userToken, wrapper))
	// }
	return s
}
