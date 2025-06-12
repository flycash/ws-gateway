package ioc

import (
	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/internal"
	"gitee.com/flycash/ws-gateway/internal/upgrader"
	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/econf"
)

func InitWebSocketServer(
	configKey string,
	q mq.MQ,
	cache ecache.Cache,
	userToken *jwt.UserToken,
	wrapper *gateway.LinkEventHandlerWrapper,
) gateway.Server {
	var compressionConfig compression.Config
	err := econf.UnmarshalKey("server.websocket.compression", &compressionConfig)
	if err != nil {
		panic(err)
	}
	partitions := econf.GetInt("pushMessageEvent.partitions")
	topic := econf.GetString("pushMessageEvent.topic")
	return internal.Load(configKey).Build(
		internal.WithMQ(q, partitions, topic),
		internal.WithCache(cache),
		internal.WithUpgrader(upgrader.New(cache, userToken, compressionConfig)),
		internal.WithLinkEventHandler(wrapper),
	)
}
