package linkevent

import (
	"context"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/event"
	"github.com/gotomicro/ego/core/elog"
)

type UserActionHandler struct {
	producer       event.UserActionEventProducer
	requestTimeout time.Duration
	logger         *elog.Component
}

func NewUserActionHandler(producer event.UserActionEventProducer, requestTimeout time.Duration) *UserActionHandler {
	return &UserActionHandler{
		producer:       producer,
		requestTimeout: requestTimeout,
		logger:         elog.EgoLogger.With(elog.FieldComponent("LinkEvent.UserActionHandler")),
	}
}

func (u *UserActionHandler) OnConnect(lk gateway.Link) error {
	err := u.sendUserActionEvent(lk, event.UserActionOnline)
	if err != nil {
		u.logger.Error("发送用户上线事件失败",
			elog.String("action", event.UserActionOnline),
			elog.String("linkID", lk.ID()),
			elog.Any("userInfo", lk.Session().UserInfo()),
			elog.FieldErr(err),
		)
	} else {
		u.logger.Info("发送用户上线事件成功",
			elog.String("action", event.UserActionOnline),
			elog.String("linkID", lk.ID()),
			elog.Any("userInfo", lk.Session().UserInfo()),
		)
	}
	return nil
}

func (u *UserActionHandler) sendUserActionEvent(lk gateway.Link, action string) error {
	userInfo := lk.Session().UserInfo()
	ctx, cancel := context.WithTimeout(context.Background(), u.requestTimeout)
	defer cancel()
	return u.producer.Produce(ctx, event.UserActionEvent{
		BizID:  userInfo.BizID,
		UserID: userInfo.UserID,
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
	err := u.sendUserActionEvent(lk, event.UserActionOffline)
	if err != nil {
		u.logger.Error("发送用户下线事件失败",
			elog.String("action", event.UserActionOffline),
			elog.String("linkID", lk.ID()),
			elog.Any("userInfo", lk.Session().UserInfo()),
			elog.FieldErr(err),
		)
	} else {
		u.logger.Info("发送用户下线事件成功",
			elog.String("action", event.UserActionOffline),
			elog.String("linkID", lk.ID()),
			elog.Any("userInfo", lk.Session().UserInfo()),
		)
	}
	return nil
}
