package linkevent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/elog"
)

const (
	ActionOnline  = "ONLINE"
	ActionOffline = "OFFLINE"
)

type UserAction struct {
	BizID  int64  `json:"bizId"`
	UserID int64  `json:"userId"`
	Action string `json:"action"` // ONLINE，OFFLINE
}

type UserActionProducer interface {
	Produce(ctx context.Context, evt UserAction) error
}

type producer struct {
	producer mq.Producer
	topic    string
}

func NewUserActionProducer(p mq.Producer, topic string) UserActionProducer {
	return &producer{
		producer: p,
		topic:    topic,
	}
}

func (p *producer) Produce(ctx context.Context, evt UserAction) error {
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

type UserActionHandler struct {
	producer       UserActionProducer
	requestTimeout time.Duration
	logger         *elog.Component
}

func NewUserActionHandler(producer UserActionProducer, requestTimeout time.Duration) *UserActionHandler {
	return &UserActionHandler{
		producer:       producer,
		requestTimeout: requestTimeout,
		logger:         elog.EgoLogger.With(elog.FieldComponent("LinkEvent.UserActionHandler")),
	}
}

func (u *UserActionHandler) OnConnect(lk gateway.Link) error {
	err := u.sendUserActionEvent(lk, ActionOnline)
	if err != nil {
		u.logger.Error("发送用户上线事件失败",
			elog.Any("action", ActionOnline),
			elog.Any("linkID", lk.ID()),
			elog.Any("session", lk.Session()),
		)
	}
	u.logger.Info("发送用户上线事件成功",
		elog.Any("action", ActionOnline),
		elog.Any("linkID", lk.ID()),
		elog.Any("session", lk.Session()),
	)
	return nil
}

func (u *UserActionHandler) sendUserActionEvent(lk gateway.Link, action string) error {
	sess := lk.Session()
	ctx, cancel := context.WithTimeout(context.Background(), u.requestTimeout)
	defer cancel()
	// todo:是否重试？
	return u.producer.Produce(ctx, UserAction{
		BizID:  sess.BizID,
		UserID: sess.UserID,
		Action: action,
	})
}

func (u *UserActionHandler) OnFrontendSendMessage(_ gateway.Link, _ []byte) error {
	return nil
}

func (u *UserActionHandler) OnBackendPushMessage(_ gateway.Link, _ *apiv1.PushMessage) error {
	return nil
}

func (u *UserActionHandler) OnDisconnect(lk gateway.Link) error {
	err := u.sendUserActionEvent(lk, ActionOffline)
	if err != nil {
		u.logger.Error("发送用户下线事件失败",
			elog.Any("action", ActionOffline),
			elog.Any("linkID", lk.ID()),
			elog.Any("session", lk.Session()),
		)
	}
	u.logger.Info("发送用户下线事件成功",
		elog.Any("action", ActionOffline),
		elog.Any("linkID", lk.ID()),
		elog.Any("session", lk.Session()),
	)
	return nil
}
