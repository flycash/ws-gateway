package ioc

import (
	"log"

	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/internal"
	"gitee.com/flycash/ws-gateway/internal/linkevent"
	"gitee.com/flycash/ws-gateway/internal/upgrader"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/ego-component/eetcd"
	"github.com/gotomicro/ego/core/econf"
)

func InitWebSocketServer(
	configKey string,
	q mq.MQ,
	localCache ecache.Cache,
	userToken *jwt.UserToken,
	codecHelper codec.Codec,
	etcdClient *eetcd.Component,
) gateway.Server {
	var compressionConfig compression.Config
	err := econf.UnmarshalKey("server.websocket.compression", &compressionConfig)
	if err != nil {
		panic(err)
	}

	partitions := econf.GetInt("pushMessageEvent.partitions")
	topic := econf.GetString("pushMessageEvent.topic")

	onReceiveTimeout := econf.GetDuration("linkEvent.onReceiveTimeout")
	initRetryInterval := econf.GetDuration("retryStrategy.initRetryInterval")
	maxRetryInterval := econf.GetDuration("retryStrategy.maxRetryInterval")
	maxRetries := econf.GetInt("retryStrategy.maxRetries")

	log.Printf("codec = %#v\n", codecHelper)

	return internal.Load(configKey).Build(
		internal.WithMQ(q, partitions, topic),
		internal.WithCache(localCache),
		internal.WithUpgrader(upgrader.New(localCache, userToken, compressionConfig)),
		internal.WithLinkEventHandler(linkevent.NewHandler(
			codecHelper,
			InitBackendClientLoader(etcdClient),
			onReceiveTimeout,
			initRetryInterval, maxRetryInterval, int32(maxRetries))),
	)
}
