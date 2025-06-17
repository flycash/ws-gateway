package ioc

import (
	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal"
	"gitee.com/flycash/ws-gateway/internal/upgrader"
	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/econf"
	"github.com/redis/go-redis/v9"
)

func InitWebSocketServer(
	configKey string,
	nodeInfo *apiv1.Node,
	q mq.MQ,
	cache ecache.Cache,
	rdb redis.Cmdable,
	userToken *jwt.UserToken,
	wrapper *gateway.LinkEventHandlerWrapper,
	registry gateway.ServiceRegistry,
	linkManager gateway.LinkManager,
) gateway.Server {
	var compressionConfig compression.Config
	err := econf.UnmarshalKey("server.websocket.compression", &compressionConfig)
	if err != nil {
		panic(err)
	}
	partitions := econf.GetInt("pushMessageEvent.partitions")
	topic := econf.GetString("pushMessageEvent.topic")
	idleTimeout := econf.GetDuration("server.websocket.autoCloseLink.idleTimeout")
	idleScanInterval := econf.GetDuration("server.websocket.autoCloseLink.idleScanInterval")
	updateNodeStateInterval := econf.GetDuration("server.websocket.registry.updateNodeStateInterval")
	return internal.Load(configKey).Build(
		internal.WithNodeInfo(nodeInfo),
		internal.WithMQ(q, partitions, topic),
		internal.WithCache(cache),
		internal.WithUpgrader(upgrader.New(rdb, userToken, compressionConfig)),
		internal.WithLinkManager(linkManager),
		internal.WithServiceRegistry(registry, updateNodeStateInterval),
		internal.WithLinkEventHandler(wrapper),
		internal.WithAutoCloseIdleLink(idleTimeout, idleScanInterval),
	)
}
