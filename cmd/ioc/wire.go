//go:build wireinject

package ioc

import (
	"fmt"

	"gitee.com/flycash/ws-gateway/internal"
	"gitee.com/flycash/ws-gateway/internal/codec"
	"gitee.com/flycash/ws-gateway/internal/id"
	"gitee.com/flycash/ws-gateway/internal/linkevent"
	"gitee.com/flycash/ws-gateway/internal/upgrader"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/google/wire"
	"github.com/gotomicro/ego/core/econf"
)

type WebsocketServer struct {
	Websocket []*internal.Component
}

// func InitServer(
// 	messageQueue mq.MQ,
// 	localCache ecache.Cache,
// 	channelServiceClient channelv1.ChannelServiceClient,
// 	messageServiceClient msgv1.MessageServiceClient,
// ) WebsocketServer {
// 	wire.Build(
// 		convertToWebsocketComponents,
//
// 		wire.Struct(new(WebsocketServer), "*"))
// 	return WebsocketServer{}
// }
//
// func convertToWebsocketComponents(
// 	messageQueue mq.MQ,
// 	localCache ecache.Cache,
// 	channelServiceClient channelv1.ChannelServiceClient,
// 	messageServiceClient msgv1.MessageServiceClient,
// ) []*internal.Component {
// 	config := econf.GetStringMap("gateway.websocket")
//
// 	serializer, ok := config["serializer"].(string)
// 	if !ok {
// 		panic("gateway.websocket.serializer配置解析错误")
// 	}
// 	delete(config, "serializer")
//
// 	codecMapping := map[string]codec.Codec{
// 		"json":     codec.NewJSONCodec(),
// 		"protobuf": codec.NewProtoCodec(),
// 	}
//
// 	s := make([]*internal.Component, 0, len(config))
// 	for i := range config {
// 		configKey := fmt.Sprintf("gateway.websocket.%s", i)
// 		s = append(s, initWebSocketGateway(configKey, codecMapping[serializer], messageQueue, localCache, channelServiceClient, messageServiceClient))
// 	}
// 	return s
// }
//
// func initWebSocketGateway(configKey string, codecHelper codec.Codec,
// 	messageQueue mq.MQ,
// 	localCache ecache.Cache,
// 	messageServiceClient msgv1.MessageServiceClient,
// ) *internal.Component {
// 	// gatewayNodeID应该让网关启动后动态获取,但是会造成循环依赖 —— linkEvent -> generator -> gateway_ID -> gateway -> linkEvent
// 	// 初始化ID生成器
// 	nodeID := econf.GetInt64(fmt.Sprintf("%s.id", configKey))
// 	generator, err := id.NewGenerator(nodeID)
// 	if err != nil {
// 		panic(err)
// 	}
// 	return internal.Load(configKey).Build(
// 		internal.WithMQ(messageQueue),
// 		internal.WithUpgrader(upgrader.New(localCache)),
// 		internal.WithLinkEventHandler(linkevent.NewHandler(generator, messageServiceClient, codecHelper)),
// 		internal.WithCache(localCache),
// 	)
// }
