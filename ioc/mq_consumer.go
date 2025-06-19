package ioc

import (
	"fmt"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/event"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/econf"
)

func InitConsumers(q mq.MQ, nodeInfo *apiv1.Node) map[string]*event.Consumer {
	return map[string]*event.Consumer{
		"pushMessage": initPushMessageConsumer(q, nodeInfo),
		"scaleUp":     initScaleUpConsumer(q, nodeInfo),
	}
}

func initPushMessageConsumer(q mq.MQ, nodeInfo *apiv1.Node) *event.Consumer {
	topic := econf.GetString("pushMessageEvent.topic")
	partitions := econf.GetInt("pushMessageEvent.partitions")
	return event.NewConsumer(name("pushMessageEvent", nodeInfo.GetId()), q, topic, partitions)
}

func initScaleUpConsumer(q mq.MQ, nodeInfo *apiv1.Node) *event.Consumer {
	topic := econf.GetString("scaleUpEvent.topic")
	partitions := econf.GetInt("scaleUpEvent.partitions")
	return event.NewConsumer(name("scaleUpEvent", nodeInfo.GetId()), q, topic, partitions)
}

func name(eventName, nodeID string) string {
	return fmt.Sprintf("%s-%s", eventName, nodeID)
}
