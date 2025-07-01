package linkevent

import (
	"context"
	"fmt"
	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/gotomicro/ego/core/elog"
)

// handleOnUpstreamMessageCmd 发送到kafka队列里
func (l *Handler) handleOnUpstreamMessageCmdV2(lk gateway.Link, msg *apiv1.Message) error {
	// 收到有意义的上行消息，更新活跃时间
	lk.UpdateActiveTime()

	bizID := l.userInfo(lk).BizID
	//
	producer, ok := l.bizToProducer.Load(bizID)
	if !ok {
		return ErrUnknownBizID
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.onReceiveTimeout)
	defer cancel()
	err := producer.Produce(ctx, msg)
	if err != nil {
		return fmt.Errorf("发消息失败: %w", err)
	}
	return l.sendUpstreamMessageAckV2(lk, msg.GetKey())
}

func (l *Handler) sendUpstreamMessageAckV2(lk gateway.Link, key string) error {
	err := l.push(lk, &apiv1.Message{
		Cmd: apiv1.Message_COMMAND_TYPE_UPSTREAM_ACK,
		// 直接用原来的 key
		Key: key,
	})
	if err != nil {
		l.logger.Error("向前端下推对上行消息的确认失败",
			elog.String("step", "sendUpstreamMessageAck"),
			elog.FieldErr(err),
		)
		return err
	}
	return nil
}
