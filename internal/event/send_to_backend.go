package event

import (
	"context"

	msgv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/ecodeclub/mq-api"
	"google.golang.org/protobuf/encoding/protojson"
)

type SendToBackendEventProducer interface {
	Produce(ctx context.Context, msg *msgv1.Message) error
}

type sendToBackendEventProducer struct {
	producer mq.Producer
	topic    string
}

func NewSendToBackendEventProducer(p mq.Producer, topic string) SendToBackendEventProducer {
	return &sendToBackendEventProducer{
		producer: p,
		topic:    topic,
	}
}

func (p *sendToBackendEventProducer) Produce(ctx context.Context, msg *msgv1.Message) error {
	v, err := protojson.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = p.producer.Produce(ctx, &mq.Message{
		Key:   []byte(msg.GetKey()),
		Value: v,
		Topic: p.topic,
	})
	return err
}
