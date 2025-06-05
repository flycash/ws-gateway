package ioc

import (
	"context"
	"log"

	"github.com/ecodeclub/mq-api"
	"github.com/ecodeclub/mq-api/kafka"
	"github.com/gotomicro/ego/core/econf"
)

func initMQ() (mq.MQ, error) {
	network := econf.GetString("mq.kafka.network")
	addresses := econf.GetStringSlice("mq.kafka.addr")
	log.Printf("initMQ: network = %#v, addr = %#v\n", network, addresses)
	queue, err := kafka.NewMQ(network, addresses)
	if err != nil {
		return nil, err
	}
	partitions := econf.GetInt("pushMessageEvent.partitions")
	topic := econf.GetString("pushMessageEvent.topic")
	log.Printf("initMQ: Topic = %#v, Partitions = %#v\n", topic, partitions)
	err = queue.CreateTopic(context.Background(), topic, partitions)
	if err != nil {
		return nil, err
	}
	return queue, nil
}
