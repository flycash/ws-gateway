package event

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ecodeclub/mq-api"
)

const (
	UserActionOnline  = "ONLINE"
	UserActionOffline = "OFFLINE"
)

type UserActionEvent struct {
	BizID  int64  `json:"bizId"`
	UserID int64  `json:"userId"`
	Action string `json:"action"` // ONLINE，OFFLINE
}

type UserActionEventProducer interface {
	Produce(ctx context.Context, evt UserActionEvent) error
}

type userActionEventProducer struct {
	producer mq.Producer
	topic    string
}

func NewUserActionEventProducer(p mq.Producer, topic string) UserActionEventProducer {
	return &userActionEventProducer{
		producer: p,
		topic:    topic,
	}
}

func (p *userActionEventProducer) Produce(ctx context.Context, evt UserActionEvent) error {
	b, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	_, err = p.producer.Produce(ctx, &mq.Message{
		Key:   []byte(fmt.Sprintf("%d-%d", evt.BizID, evt.UserID)),
		Value: b,
		Topic: p.topic,
	})
	return err
}
