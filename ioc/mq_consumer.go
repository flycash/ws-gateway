package ioc

import (
	"gitee.com/flycash/ws-gateway/internal/event"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/econf"
)

func InitConsumers(q mq.MQ) map[string]*event.Consumer {
	return map[string]*event.Consumer{
		"pushMessage": initPushMessageConsumer(q),
		"scaleUp":     initScaleUpConsumer(q),
	}
}

func initPushMessageConsumer(q mq.MQ) *event.Consumer {
	topic := econf.GetString("pushMessageEvent.topic")
	partitions := econf.GetInt("pushMessageEvent.partitions")
	return event.NewConsumer("pushMessageEvent", q, topic, partitions)
}

func initScaleUpConsumer(q mq.MQ) *event.Consumer {
	topic := econf.GetString("scaleUpEvent.topic")
	partitions := econf.GetInt("scaleUpEvent.partitions")
	return event.NewConsumer("scaleUpEvent", q, topic, partitions)
}
