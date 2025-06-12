package linkevent

import (
	"fmt"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/gotomicro/ego/core/elog"
	"github.com/prometheus/client_golang/prometheus"
)

type OnlineUserHandler struct {
	counter *prometheus.CounterVec
	logger  *elog.Component
}

func NewOnlineUserHandler() *OnlineUserHandler {
	registry := prometheus.DefaultRegisterer
	counter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "websocket_gateway",
			Name:      "online_users_total",
			Help:      "Total number of WebSocket Gateway users.",
		},
		[]string{"biz_id", "user_id"},
	)
	// 注册指标
	registry.MustRegister(counter)

	return &OnlineUserHandler{
		counter: counter,
		logger:  elog.EgoLogger.With(elog.FieldComponent("LinkEvent.OnlineUserHandler")),
	}
}

func (o *OnlineUserHandler) OnConnect(lk gateway.Link) error {
	labelValues := o.labelValues(lk)
	o.counter.WithLabelValues(labelValues...).Inc()
	o.logger.Info("统计在线用户总数：+1", elog.Any("labelValues", labelValues))
	return nil
}

func (o *OnlineUserHandler) labelValues(lk gateway.Link) []string {
	sess := lk.Session()
	return []string{fmt.Sprintf("%d", sess.BizID), fmt.Sprintf("%d", sess.UserID)}
}

func (o *OnlineUserHandler) OnFrontendSendMessage(_ gateway.Link, _ []byte) error {
	return nil
}

func (o *OnlineUserHandler) OnBackendPushMessage(_ gateway.Link, _ *apiv1.PushMessage) error {
	return nil
}

func (o *OnlineUserHandler) OnDisconnect(lk gateway.Link) error {
	labelValues := o.labelValues(lk)
	o.counter.WithLabelValues(labelValues...).Desc()
	o.logger.Info("统计在线用户总数：-1", elog.Any("labelValues", labelValues))
	return nil
}
