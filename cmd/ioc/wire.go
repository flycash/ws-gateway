//go:build wireinject

package ioc

import (
	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/event"
	"gitee.com/flycash/ws-gateway/internal/limiter"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/pkg/jwt"
	scalerpkg "gitee.com/flycash/ws-gateway/pkg/scaler"
	"github.com/cenkalti/backoff/v5"
	"github.com/redis/go-redis/v9"

	"gitee.com/flycash/ws-gateway/ioc"
	"github.com/ecodeclub/ecache"
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

		ioc.InitConsumers,
		ioc.InitScaleUpEventProducer,
		ioc.InitUserActionEventProducer,
		ioc.InitLinkEventHandlerWrapper,
		ioc.InitLinkManager,
		ioc.InitRegistry,
		ioc.InitTokenLimiter,
		ioc.InitExponentialBackOff,

		// Docker 扩容相关
		ioc.InitDockerClient,
		ioc.InitDockerScaler,

		convertToWebsocketComponents,

		wire.Struct(new(App), "*"))
	return App{}
}

func convertToWebsocketComponents(
	nodeInfo *apiv1.Node,
	c ecache.Cache,
	rdb redis.Cmdable,
	userToken *jwt.UserToken,
	wrapper *gateway.LinkEventHandlerWrapper,
	registry gateway.ServiceRegistry,
	linkManager *link.Manager,
	tokenLimiter *limiter.TokenLimiter,
	backoff *backoff.ExponentialBackOff,
	consumers map[string]*event.Consumer,
	producer event.ScaleUpEventProducer,
	scaler scalerpkg.Scaler,
) []gateway.Server {
	configKey := "server.websocket"
	s := make([]gateway.Server, 0, 2)
	// for i := range config {
	// configKey := fmt.Sprintf("server.websocket.%d", i)
	s = append(s, ioc.InitWebSocketServer(
		configKey,
		nodeInfo,
		c,
		rdb,
		userToken,
		wrapper,
		registry,
		linkManager,
		tokenLimiter,
		backoff,
		consumers,
	))
	s = append(s, ioc.InitWebhookServer(nodeInfo, registry, linkManager, producer, scaler))
	// }
	return s
}
