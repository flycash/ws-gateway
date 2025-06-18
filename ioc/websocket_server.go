package ioc

import (
	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal"
	"gitee.com/flycash/ws-gateway/internal/event"
	"gitee.com/flycash/ws-gateway/internal/limiter"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/internal/upgrader"
	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"github.com/cenkalti/backoff/v5"
	"github.com/ecodeclub/ecache"
	"github.com/gotomicro/ego/core/econf"
	"github.com/redis/go-redis/v9"
)

func InitWebSocketServer(
	configKey string,
	nodeInfo *apiv1.Node,
	cache ecache.Cache,
	rdb redis.Cmdable,
	userToken *jwt.UserToken,
	wrapper *gateway.LinkEventHandlerWrapper,
	registry gateway.ServiceRegistry,
	linkManager *link.Manager,
	tokenLimiter *limiter.TokenLimiter,
	backoff *backoff.ExponentialBackOff,
	consumers map[string]*event.Consumer,
) gateway.Server {
	var compressionConfig compression.Config
	err := econf.UnmarshalKey("server.websocket.compression", &compressionConfig)
	if err != nil {
		panic(err)
	}

	idleTimeout := econf.GetDuration("server.websocket.autoCloseLink.idleTimeout")
	idleScanInterval := econf.GetDuration("server.websocket.autoCloseLink.idleScanInterval")
	updateNodeStateInterval := econf.GetDuration("server.websocket.registry.updateNodeStateInterval")
	return internal.Load(configKey).Build(
		internal.WithNodeInfo(nodeInfo),
		// internal.WithMQ(q, partitions, topic),
		internal.WithConsumers(consumers),
		internal.WithCache(cache),
		internal.WithUpgrader(upgrader.New(rdb, userToken, compressionConfig)),
		internal.WithLinkManager(linkManager),
		internal.WithServiceRegistry(registry, updateNodeStateInterval),
		internal.WithLinkEventHandler(wrapper),
		internal.WithAutoCloseIdleLink(idleTimeout, idleScanInterval),
		internal.WithTokenLimiter(tokenLimiter),
		internal.WithExponentialBackOff(backoff),
	)
}
