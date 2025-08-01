package linkevent

import (
	"strconv"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/gotomicro/ego/core/elog"
	"github.com/prometheus/client_golang/prometheus"
)

type OnlineUserHandler struct {
	counter *prometheus.GaugeVec
	logger  *elog.Component
}

func NewOnlineUserHandler() *OnlineUserHandler {
	registry := prometheus.DefaultRegisterer
	counter := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "websocket_gateway",
			Name:      "online_users",
			Help:      "当前在线的WebSocket网关用户数。",
		},
		[]string{"biz_id"},
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
	userInfo := lk.Session().UserInfo()
	return []string{
		strconv.FormatInt(userInfo.BizID, 10),
	}
}

func (o *OnlineUserHandler) OnFrontendSendMessage(_ gateway.Link, _ []byte) error {
	return nil
}

func (o *OnlineUserHandler) OnBackendPushMessage(_ gateway.Link, _ *apiv1.PushMessage) error {
	return nil
}

func (o *OnlineUserHandler) OnDisconnect(lk gateway.Link) error {
	labelValues := o.labelValues(lk)
	o.counter.WithLabelValues(labelValues...).Dec()
	o.logger.Info("统计在线用户总数：-1", elog.Any("labelValues", labelValues))
	return nil
}
