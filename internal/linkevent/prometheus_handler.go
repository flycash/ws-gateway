package linkevent

import (
	"strconv"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"github.com/gotomicro/ego/core/elog"
	"github.com/prometheus/client_golang/prometheus"
)

type PrometheusHandler struct {
	codecHelper        codec.Codec
	messageCounter     *prometheus.CounterVec
	messageSizeCounter *prometheus.CounterVec
	logger             *elog.Component
}

func NewPrometheusHandler(codecHelper codec.Codec) *PrometheusHandler {
	ph := &PrometheusHandler{
		codecHelper: codecHelper,
		messageCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "websocket_gateway",
				Name:      "messages_total",
				Help:      "上行和下行消息总数。",
			},
			[]string{"direction", "biz_id"},
		),
		messageSizeCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "websocket_gateway",
				Name:      "message_size_bytes_total",
				Help:      "消息体的总字节大小。",
			},
			[]string{"direction", "biz_id"},
		),
		logger: elog.EgoLogger.With(elog.FieldComponent("LinkEvent.PrometheusHandler")),
	}
	prometheus.DefaultRegisterer.MustRegister(ph.messageCounter, ph.messageSizeCounter)
	return ph
}

func (h *PrometheusHandler) OnConnect(_ gateway.Link) error {
	return nil
}

func (h *PrometheusHandler) OnFrontendSendMessage(lk gateway.Link, payload []byte) error {
	msg := &apiv1.Message{}
	// 反序列化
	err := h.codecHelper.Unmarshal(payload, msg)
	if err != nil {
		//nolint:nilerr // 无法解析的消息，暂不统计
		return nil
	}
	// 心跳消息不统计
	if msg.GetCmd() == apiv1.Message_COMMAND_TYPE_HEARTBEAT {
		return nil
	}
	bizID := lk.Session().UserInfo().BizID
	h.updateMessageMetrics("upstream", bizID, len(msg.GetBody()))
	return nil
}

func (h *PrometheusHandler) updateMessageMetrics(direction string, bizID int64, bodySize int) {
	bizIDStr := strconv.FormatInt(bizID, 10)
	h.messageCounter.WithLabelValues(direction, bizIDStr).Inc()
	h.messageSizeCounter.WithLabelValues(direction, bizIDStr).Add(float64(bodySize))
}

func (h *PrometheusHandler) OnBackendPushMessage(_ gateway.Link, msg *apiv1.PushMessage) error {
	bizID := msg.GetBizId()
	h.updateMessageMetrics("downstream", bizID, len(msg.GetBody()))
	return nil
}

func (h *PrometheusHandler) OnDisconnect(_ gateway.Link) error {
	return nil
}
