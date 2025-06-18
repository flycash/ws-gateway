package ioc

import (
	"gitee.com/flycash/ws-gateway/internal/event"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/econf"
)

func InitUserActionEventProducer(q mq.MQ) event.UserActionEventProducer {
	type Config struct {
		Topic string `yaml:"topic"`
	}
	var cfg Config
	err := econf.UnmarshalKey("userActionEvent", &cfg)
	if err != nil {
		panic(err)
	}
	producer, err := q.Producer(cfg.Topic)
	if err != nil {
		panic(err)
	}
	return event.NewUserActionEventProducer(producer, cfg.Topic)
}

func InitScaleUpEventProducer(q mq.MQ) event.ScaleUpEventProducer {
	type Config struct {
		Topic string `yaml:"topic"`
	}
	var cfg Config
	err := econf.UnmarshalKey("scaleUpEvent", &cfg)
	if err != nil {
		panic(err)
	}
	producer, err := q.Producer(cfg.Topic)
	if err != nil {
		panic(err)
	}
	return event.NewScaleUpEventProducer(producer, cfg.Topic)
}
