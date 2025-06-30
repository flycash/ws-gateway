package linkevent

import (
	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"github.com/gotomicro/ego/core/elog"
	"golang.org/x/time/rate"
)

type LimitHandler struct {
	codecHelper codec.Codec
	limiter     *rate.Limiter
	logger      *elog.Component
}

func NewLimitHandler(codecHelper codec.Codec, limiter *rate.Limiter) *LimitHandler {
	return &LimitHandler{
		codecHelper: codecHelper,
		limiter:     limiter,
		logger:      elog.EgoLogger.With(elog.FieldComponent("LinkEvent.LimitHandler")),
	}
}

func (h *LimitHandler) OnConnect(_ gateway.Link) error {
	return nil
}

func (h *LimitHandler) OnFrontendSendMessage(lk gateway.Link, payload []byte) error {
	h.logger.Info("判断是否需要被限流......")

	msg := &apiv1.Message{}
	// 反序列化
	err := h.codecHelper.Unmarshal(payload, msg)
	if err != nil {
		//nolint:nilerr // 无法解析的消息，放行给后续的 handler 处理
		return nil
	}
	// 心跳消息不作限制
	if msg.GetCmd() == apiv1.Message_COMMAND_TYPE_HEARTBEAT {
		return nil
	}

	if !h.limiter.Allow() {
		// 告知前端被限流了
		_ = lk.Send(h.rateLimitExceededMessage(msg.GetKey()))
		h.logger.Warn("超过最大限制，限流中......")
		// 返回哨兵错误，中断后续 handler 调用
		return gateway.ErrRateLimitExceeded
	}

	h.logger.Info("无需限流，放行请求......")
	return nil
}

func (h *LimitHandler) rateLimitExceededMessage(key string) []byte {
	msg := &apiv1.Message{
		Cmd: apiv1.Message_COMMAND_TYPE_RATE_LIMIT_EXCEEDED,
		Key: key,
	}
	// 忽略错误，因为这个消息结构非常简单，理论上不会出错
	payload, _ := h.codecHelper.Marshal(msg)
	return payload
}

func (h *LimitHandler) OnBackendPushMessage(_ gateway.Link, _ *apiv1.PushMessage) error {
	// 暂时不对下行消息做限制
	return nil
}

func (h *LimitHandler) OnDisconnect(_ gateway.Link) error {
	return nil
}
