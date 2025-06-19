package event

import (
	"context"
	"encoding/json"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/ecodeclub/mq-api"
)

// ScaleUpEvent SCALE_UP 事件所需的具体数据。
type ScaleUpEvent struct {
	TotalNodeCount int64           `json:"totalNodeCount"`
	NewNodeList    *apiv1.NodeList `json:"newNodeList"`
}
type ScaleUpEventProducer interface {
	Produce(ctx context.Context, evt ScaleUpEvent) error
}

type scaleUpEventProducer struct {
	producer mq.Producer
	topic    string
}

func NewScaleUpEventProducer(p mq.Producer, topic string) ScaleUpEventProducer {
	return &scaleUpEventProducer{
		producer: p,
		topic:    topic,
	}
}

func (p *scaleUpEventProducer) Produce(ctx context.Context, evt ScaleUpEvent) error {
	b, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	_, err = p.producer.Produce(ctx, &mq.Message{
		Value: b,
		Topic: p.topic,
	})
	return err
}
