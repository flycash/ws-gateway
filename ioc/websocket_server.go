package ioc

import (
	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/internal"
	"gitee.com/flycash/ws-gateway/internal/codec"
	"gitee.com/flycash/ws-gateway/internal/linkevent"
	"gitee.com/flycash/ws-gateway/internal/upgrader"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/ego-component/eetcd"
	"github.com/gotomicro/ego/core/econf"
)

func InitWebSocketServer(
	configKey string,
	q mq.MQ,
	localCache ecache.Cache,
	codecHelper codec.Codec,
	etcdClient *eetcd.Component,
) gateway.Server {
	partitions := econf.GetInt("pushMessageEvent.partitions")
	topic := econf.GetString("pushMessageEvent.topic")

	initRetryInterval := econf.GetDuration("retryStrategy.initRetryInterval")
	maxRetryInterval := econf.GetDuration("retryStrategy.maxRetryInterval")
	maxRetries := econf.GetInt("retryStrategy.maxRetries")

	return internal.Load(configKey).Build(
		internal.WithMQ(q, partitions, topic),
		internal.WithCache(localCache),
		internal.WithUpgrader(upgrader.New(localCache)),
		internal.WithLinkEventHandler(linkevent.NewHandler(
			codecHelper,
			InitBackendClientLoader(etcdClient),
			initRetryInterval, maxRetryInterval, int32(maxRetries))),
	)
}
