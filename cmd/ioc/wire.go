//go:build wireinject

package ioc

import (
	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/limiter"
	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"github.com/cenkalti/backoff/v5"
	"github.com/redis/go-redis/v9"

	"gitee.com/flycash/ws-gateway/ioc"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/google/wire"
)

type App struct {
	OrderServer []gateway.Server
}

func InitApp(nodeInfo *apiv1.Node) App {
	wire.Build(
		ioc.InitRedisCmd,
		ioc.InitRedisCache,
		ioc.InitUserToken,
		ioc.InitEtcdClient,
		ioc.InitMQ,
		ioc.InitSerializer,

		ioc.InitLinkEventHandlerWrapper,
		ioc.InitLinkManager,
		ioc.InitRegistry,
		ioc.InitTokenLimiter,
		ioc.InitExponentialBackOff,

		convertToWebsocketComponents,

		wire.Struct(new(App), "*"))
	return App{}
}

func convertToWebsocketComponents(
	nodeInfo *apiv1.Node,
	messageQueue mq.MQ,
	c ecache.Cache,
	rdb redis.Cmdable,
	userToken *jwt.UserToken,
	wrapper *gateway.LinkEventHandlerWrapper,
	registry gateway.ServiceRegistry,
	linkManager gateway.LinkManager,
	tokenLimiter *limiter.TokenLimiter,
	backoff *backoff.ExponentialBackOff,
) []gateway.Server {
	configKey := "server.websocket"
	s := make([]gateway.Server, 0, 1)
	// for i := range config {
	// configKey := fmt.Sprintf("server.websocket.%d", i)
	s = append(s, ioc.InitWebSocketServer(
		configKey,
		nodeInfo,
		messageQueue,
		c,
		rdb,
		userToken,
		wrapper,
		registry,
		linkManager,
		tokenLimiter,
		backoff,
	))
	// }
	return s
}
