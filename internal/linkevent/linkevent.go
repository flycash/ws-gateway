package linkevent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/codec"
	"github.com/ecodeclub/ekit/retry"
	"github.com/ecodeclub/ekit/syncx"
	"github.com/gotomicro/ego/core/elog"
	"google.golang.org/protobuf/types/known/anypb"
)

var (
	ErrUnKnownFrontendMessageFormat      = errors.New("非法的网关消息格式")
	ErrUnKnownFrontendMessageCommandType = errors.New("非法的网关消息Cmd类型")

	ErrMaxRetriesExceeded = errors.New("最大重试次数已耗尽")
	ErrUnknownBizID       = errors.New("未知的BizID")

	ErrUnKnownBackendMessageFormat = errors.New("非法的下推消息格式")
)

type BackendClientLoader func() *syncx.Map[int64, apiv1.BackendServiceClient]

type Handler struct {
	codecHelper                     codec.Codec
	onFrontendSendMessageHandleFunc map[apiv1.Message_CommandType]func(lk gateway.Link, msg *apiv1.Message) error

	backendClientLoader BackendClientLoader
	bizToClient         *syncx.Map[int64, apiv1.BackendServiceClient]

	initRetryInterval time.Duration
	maxRetryInterval  time.Duration
	maxRetries        int32

	logger *elog.Component
}

// NewHandler 创建一个Link生命周期事件管理器
func NewHandler(
	codecHelper codec.Codec,
	backendClientLoader BackendClientLoader,
	initRetryInterval time.Duration,
	maxRetryInterval time.Duration,
	maxRetries int32,
) *Handler {
	h := &Handler{
		codecHelper:                     codecHelper,
		onFrontendSendMessageHandleFunc: make(map[apiv1.Message_CommandType]func(lk gateway.Link, msg *apiv1.Message) error),
		backendClientLoader:             backendClientLoader,
		bizToClient:                     &syncx.Map[int64, apiv1.BackendServiceClient]{},
		initRetryInterval:               initRetryInterval,
		maxRetryInterval:                maxRetryInterval,
		maxRetries:                      maxRetries,
		logger:                          elog.EgoLogger.With(elog.FieldComponent("LinkEventHandler")),
	}

	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_HEARTBEAT] = h.handleOnHeartbeatCmd
	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_UPSTREAM_MESSAGE] = h.handleOnUpstreamMessageCmd
	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_DOWNSTREAM_ACK] = h.handleDownstreamAckCmd

	return h
}

func (l *Handler) OnConnect(lk gateway.Link) error {
	// 验证Auth包、协商序列化算法、加密算法、压缩算法
	l.logger.Debug("Hello link = %s", elog.String("lid", lk.ID()))
	return nil
}

// OnFrontendSendMessage 统一处理前端发来的各种请求
func (l *Handler) OnFrontendSendMessage(lk gateway.Link, payload []byte) error {
	msg := &apiv1.Message{}
	err := l.codecHelper.Unmarshal(payload, msg)
	if err != nil || msg.GetBizId() == 0 || msg.GetKey() == "" {
		l.logger.Error("反序列化消息失败",
			elog.String("step", "OnFrontendSendMessage"),
			elog.String("linkID", lk.ID()),
			elog.Any("session", lk.Session()),
			elog.FieldErr(err),
		)
		return fmt.Errorf("%w", ErrUnKnownFrontendMessageFormat)
	}

	l.logger.Info("OnFrontendSendMessage",
		elog.String("step", "前端发送的消息(上行消息+对下行消息的响应)"),
		elog.String("消息体", msg.String()))

	// 前端发送的消息(心跳、上行消息及对下行消息的确认) 统一在这里处理
	handleFunc, ok := l.onFrontendSendMessageHandleFunc[msg.Cmd]
	if !ok {
		l.logger.Error("前端发送未知消息类型",
			elog.String("step", "OnFrontendSendMessage"),
			elog.String("linkID", lk.ID()),
			elog.Any("session", lk.Session()),
		)
		return fmt.Errorf("%w", ErrUnKnownFrontendMessageCommandType)
	}
	return handleFunc(lk, msg)
}

// handleOnHeartbeatCmd 处理前端发来的“心跳”请求
func (l *Handler) handleOnHeartbeatCmd(lk gateway.Link, msg *apiv1.Message) error {
	// 心跳包原样返回
	return l.push(lk, msg)
}

func (l *Handler) push(lk gateway.Link, msg *apiv1.Message) error {
	payload, err := l.codecHelper.Marshal(msg)
	if err != nil {
		l.logger.Error("序列化网关消息失败",
			elog.String("step", "push"),
			elog.String("消息体", msg.String()),
			elog.FieldErr(err),
		)
		return err
	}
	err = lk.Send(payload)
	if err != nil {
		l.logger.Error("通过link对象下推消息给前端用户失败",
			elog.String("step", "push"),
			elog.String("消息体", msg.String()),
			elog.String("linkID", lk.ID()),
			elog.Any("session", lk.Session()),
			elog.FieldErr(err))
		return err
	}
	return nil
}

// handleOnUpstreamMessageCmd 处理前端发来的“上行业务消息”请求
func (l *Handler) handleOnUpstreamMessageCmd(lk gateway.Link, msg *apiv1.Message) error {
	resp, err := l.forwardToBusinessBackend(msg)
	if err != nil {
		// 向业务后端转发失败，（包含已经重试）如何处理？ 这里返回err相当于丢掉了，等待前端超时重试
		l.logger.Warn("向业务后端转发消息失败",
			elog.String("step", "OnFrontendSendMessage"),
			elog.String("step", "handleOnUpstreamMessageCmd"),
			elog.FieldErr(err),
		)
		return err
	}
	return l.sendUpstreamMessageAck(lk, resp)
}

func (l *Handler) forwardToBusinessBackend(msg *apiv1.Message) (*apiv1.OnReceiveResponse, error) {
	retryStrategy, _ := retry.NewExponentialBackoffRetryStrategy(l.initRetryInterval, l.maxRetryInterval, l.maxRetries)
	var loaded bool
	for {
		client, found := l.bizToClient.Load(msg.BizId)
		if !found {
			if !loaded {
				// l.bizToClient 中未找到，并且未重新加载过，重新加载业务后端GRPC客户端
				l.bizToClient = l.backendClientLoader()
				loaded = true
				continue
			}
			return nil, fmt.Errorf("%w: %d", ErrUnknownBizID, msg.BizId)
		}
		resp, err := client.OnReceive(context.Background(), &apiv1.OnReceiveRequest{
			Key:  msg.GetKey(),
			Body: msg.GetBody(),
		})
		if err == nil {
			return resp, nil
		}
		duration, ok := retryStrategy.Next()
		if !ok {
			return nil, fmt.Errorf("%w: %w", err, ErrMaxRetriesExceeded)
		}
		time.Sleep(duration)
	}
}

func (l *Handler) sendUpstreamMessageAck(lk gateway.Link, resp *apiv1.OnReceiveResponse) error {
	// 将业务后端返回的“上行消息”的响应直接封装为body
	respBody, _ := anypb.New(resp)
	err := l.push(lk, &apiv1.Message{
		Cmd:   apiv1.Message_COMMAND_TYPE_UPSTREAM_ACK,
		BizId: resp.GetBizId(),
		Key:   fmt.Sprintf("%d-%d", resp.GetBizId(), resp.GetMsgId()),
		Body:  respBody,
	})
	if err != nil {
		l.logger.Error("向前端下推对上行消息的确认失败",
			elog.String("step", "OnFrontendSendMessage"),
			elog.String("step", "sendUpstreamMessageAck"),
			elog.FieldErr(err),
		)
		return fmt.Errorf("%w", err)
	}
	return nil
}

// handleDownstreamAckCmd 处理前端发来的“对下行消息的确认”请求
func (l *Handler) handleDownstreamAckCmd(_ gateway.Link, _ *apiv1.Message) error {
	// 这里可以考虑通知业务后端下行消息的发送结果 如 使用 BackendService.OnPushed 方法
	// 也可以考虑使用消息队列通知业务后端，规避GRPC客户端的各种重试、超时问题，并保证高吞吐量
	return nil
}

// OnBackendPushMessage 统一处理各个业务后端发来的下推请求
func (l *Handler) OnBackendPushMessage(lk gateway.Link, msg *apiv1.PushMessage) error {
	if msg.GetBizId() == 0 || msg.GetKey() == "" {
		return fmt.Errorf("%w", ErrUnKnownBackendMessageFormat)
	}

	err := l.push(lk, &apiv1.Message{
		Cmd:   apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
		BizId: msg.GetBizId(),
		Key:   msg.GetKey(),
		Body:  msg.GetBody(),
	})
	if err != nil {
		l.logger.Error("向前端推送下行消息失败",
			elog.String("step", "OnBackendPushMessage"),
			elog.FieldErr(err),
		)
		return fmt.Errorf("%w", err)
	}
	return nil
}

func (l *Handler) OnDisconnect(lk gateway.Link) error {
	// 退出清理操作
	log.Printf("Goodbye link = %s!\n", lk.ID())
	return nil
}
