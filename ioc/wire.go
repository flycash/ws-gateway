//go:build wireinject

package ioc

import (
	"fmt"

	channelv1 "gitee.com/flycash/ws-gateway/api/proto/gen/channel/v1"
	msgv1 "gitee.com/flycash/ws-gateway/api/proto/gen/msg/v1"
	"gitee.com/flycash/ws-gateway/websocket"
	"gitee.com/flycash/ws-gateway/websocket/codec"
	"gitee.com/flycash/ws-gateway/websocket/id"
	"gitee.com/flycash/ws-gateway/websocket/linkevent"
	"gitee.com/flycash/ws-gateway/websocket/upgrader"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/google/wire"
	"github.com/gotomicro/ego/core/econf"
)

type WebsocketServer struct {
	Websocket []*websocket.Component
}

func InitServer(
	messageQueue mq.MQ,
	localCache ecache.Cache,
	channelServiceClient channelv1.ChannelServiceClient,
	messageServiceClient msgv1.MessageServiceClient,
) WebsocketServer {
	wire.Build(
		convertToWebsocketComponents,

		wire.Struct(new(WebsocketServer), "*"))
	return WebsocketServer{}
}

func convertToWebsocketComponents(
	messageQueue mq.MQ,
	localCache ecache.Cache,
	channelServiceClient channelv1.ChannelServiceClient,
	messageServiceClient msgv1.MessageServiceClient,
) []*websocket.Component {
	config := econf.GetStringMap("gateway.websocket")

	serializer, ok := config["serializer"].(string)
	if !ok {
		panic("gateway.websocket.serializer配置解析错误")
	}
	delete(config, "serializer")

	codecMapping := map[string]codec.Codec{
		"json":     codec.NewJSONCodec(),
		"protobuf": codec.NewProtoCodec(),
	}

	s := make([]*websocket.Component, 0, len(config))
	for i := range config {
		configKey := fmt.Sprintf("gateway.websocket.%s", i)
		s = append(s, initWebSocketGateway(configKey, codecMapping[serializer], messageQueue, localCache, channelServiceClient, messageServiceClient))
	}
	return s
}

func initWebSocketGateway(configKey string, codecHelper codec.Codec,
	messageQueue mq.MQ,
	localCache ecache.Cache,
	channelServiceClient channelv1.ChannelServiceClient,
	messageServiceClient msgv1.MessageServiceClient,
) *websocket.Component {
	// gatewayNodeID应该让网关启动后动态获取,但是会造成循环依赖 —— linkEvent -> generator -> gateway_ID -> gateway -> linkEvent
	// 初始化ID生成器
	nodeID := econf.GetInt64(fmt.Sprintf("%s.id", configKey))
	generator, err := id.NewGenerator(nodeID)
	if err != nil {
		panic(err)
	}
	return websocket.Load(configKey).Build(
		websocket.WithMQ(messageQueue),
		websocket.WithUpgrader(upgrader.New(localCache, channelServiceClient)),
		websocket.WithLinkEventHandler(linkevent.NewHandler(generator, messageServiceClient, codecHelper)),
		websocket.WithCache(localCache),
	)
}
