package linkevent

import (
	"context"
	"errors"
	"fmt"
	"log"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/codec"
	"gitee.com/flycash/ws-gateway/internal/id"
	"gitee.com/flycash/ws-gateway/internal/pkg/grpc"
	"github.com/ecodeclub/ekit/syncx"
	"github.com/gotomicro/ego/core/elog"
	"google.golang.org/protobuf/types/known/anypb"
)

var (
	_                                    gateway.LinkEventHandler = &linkEventHandler{}
	ErrUnKnownFrontendMessageFormat                               = errors.New("非法的网关API消息格式")
	ErrUnKnownFrontendMessageCommandType                          = errors.New("非法的网关API消息Cmd类型")
	ErrUnKnownFrontendMessageBodyType                             = errors.New("非法的网关API消息Body类型")

	ErrMaxRetriesExceeded = errors.New("最大重试次数已耗尽")
	ErrUnknownBizID       = errors.New("未知的BizID")

	ErrUnKnownBackendMessageFormat   = errors.New("非法的msg服务消息格式")
	ErrUnKnownBackendMessageType     = errors.New("非法的msg服务消息Type类型")
	ErrUnKnownBackendMessagePushID   = errors.New("非法的msg服务消息PushId")
	ErrUnKnownBackendMessageBodyType = errors.New("非法的msg服务消息Body类型")
)

type linkEventHandler struct {
	idGenerator                     id.Generator
	codecHelper                     codec.Codec
	onFrontendSendMessageHandleFunc map[apiv1.Message_CommandType]func(lk gateway.Link, msg *apiv1.Message) error
	bizToServiceName                *syncx.Map[int64, string]
	bizBackendClients               *grpc.Clients[apiv1.BackendServiceClient]

	logger *elog.Component
}

// NewHandler 创建一个Link生命周期事件管理器
func NewHandler(
	idGenerator id.Generator,
	codecHelper codec.Codec,
	bizToServiceName *syncx.Map[int64, string],
	bizBackendClients *grpc.Clients[apiv1.BackendServiceClient],
) gateway.LinkEventHandler {
	h := &linkEventHandler{
		idGenerator:                     idGenerator,
		codecHelper:                     codecHelper,
		onFrontendSendMessageHandleFunc: make(map[apiv1.Message_CommandType]func(lk gateway.Link, msg *apiv1.Message) error),
		// onBackendPushHandleFunc:  make(map[msgv1.Message_Type]func(lk gateway.Link, message *msgv1.Message) error),
		bizToServiceName: bizToServiceName,
		// bizBackendClients: grpc.NewClients(func(conn *egrpc.Component) apiv1.BackendServiceClient {
		// 	return apiv1.NewBackendServiceClient(conn)
		// }),
		bizBackendClients: bizBackendClients,
		logger:            elog.EgoLogger.With(elog.FieldComponent("LinkEventHandler")),
	}

	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_HEARTBEAT] = h.handleOnHeartbeatCmd
	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_UPSTREAM_MESSAGE] = h.handleOnUpstreamMessageCmd
	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_DOWNSTREAM_ACK] = h.handleDownstreamAckCmd

	return h
}

func (l *linkEventHandler) OnConnect(lk gateway.Link) error {
	// 验证Auth包
	l.logger.Debug("Hello link = %s", elog.String("lid", lk.ID()))
	return nil
}

// OnFrontendSendMessage 统一处理前端发来的各种请求
func (l *linkEventHandler) OnFrontendSendMessage(lk gateway.Link, payload []byte) error {
	msg := &apiv1.Message{}
	err := l.codecHelper.Unmarshal(payload, msg)
	if err != nil {
		l.logger.Error("反序列化消息失败",
			elog.String("step", "OnFrontendSendMessage"),
			elog.String("linkID", lk.ID()),
			elog.Int64("bizID", lk.BizID()),
			elog.Int64("userID", lk.UserID()),
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
			elog.Int64("bizID", lk.BizID()),
			elog.Int64("userID", lk.UserID()),
		)
		return fmt.Errorf("%w", ErrUnKnownFrontendMessageCommandType)
	}
	return handleFunc(lk, msg)
}

// handleOnHeartbeatCmd 处理前端发来的“心跳”请求
func (l *linkEventHandler) handleOnHeartbeatCmd(lk gateway.Link, msg *apiv1.Message) error {
	// 心跳包原样返回
	return l.push(lk, msg)
}

func (l *linkEventHandler) push(lk gateway.Link, msg *apiv1.Message) error {
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
			elog.Int64("bizID", lk.BizID()),
			elog.Int64("userID", lk.UserID()),
			elog.FieldErr(err))
		return err
	}
	return nil
}

// handleOnUpstreamMessageCmd 处理前端发来的“上行业务消息”请求
func (l *linkEventHandler) handleOnUpstreamMessageCmd(lk gateway.Link, msg *apiv1.Message) error {
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

func (l *linkEventHandler) forwardToBusinessBackend(msg *apiv1.Message) (*apiv1.OnReceiveResponse, error) {
	// retryStrategy, _ := retry.NewExponentialBackoffRetryStrategy(time.Second, time.Second, 3)
	// for {
	// 	// todo: 通过bizID找到服务名再找到后端服务的client
	// 	serviceName, found := l.bizToServiceName.Load(msg.BizId)
	// 	if !found {
	// 		return nil, fmt.Errorf("%w: %d", ErrUnknownBizID, msg.BizId)
	// 	}
	// 	client := l.bizBackendClients.Get(serviceName)
	// 	resp, err := client.OnReceive(context.Background(), &apiv1.OnReceiveRequest{
	// 		Key:  msg.GetKey(),
	// 		Body: msg.GetBody(),
	// 	})
	// 	if err == nil {
	// 		return resp, nil
	// 	}
	// 	duration, ok := retryStrategy.Next()
	// 	if !ok {
	// 		return nil, fmt.Errorf("%w", ErrMaxRetriesExceeded)
	// 	}
	// 	time.Sleep(duration)
	// }
	// todo: 要在这里重试吗？还是server component中？
	serviceName, found := l.bizToServiceName.Load(msg.BizId)
	if !found {
		return nil, fmt.Errorf("%w: %d", ErrUnknownBizID, msg.BizId)
	}
	client := l.bizBackendClients.Get(serviceName)
	return client.OnReceive(context.Background(), &apiv1.OnReceiveRequest{
		Key:  msg.GetKey(),
		Body: msg.GetBody(),
	})
}

func (l *linkEventHandler) sendUpstreamMessageAck(lk gateway.Link, resp *apiv1.OnReceiveResponse) error {
	// todo: 对上行业务消息的响应是这样构造吗？
	respBody, _ := anypb.New(resp)
	err := l.push(lk, &apiv1.Message{
		Cmd:  apiv1.Message_COMMAND_TYPE_UPSTREAM_ACK,
		Body: respBody,
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
func (l *linkEventHandler) handleDownstreamAckCmd(_ gateway.Link, _ *apiv1.Message) error {
	// todo：下行推送的消息前端已经收到，做一些后续处理，比如停止一直重发的go协程？
	return nil
}

// OnBackendPushMessage 统一处理各个业务后端发来的下推请求
func (l *linkEventHandler) OnBackendPushMessage(lk gateway.Link, msg *apiv1.PushMessage) error {
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

func (l *linkEventHandler) OnDisconnect(lk gateway.Link) error {
	// 退出清理操作
	log.Printf("Goodbye link = %s!\n", lk.ID())
	return nil
}
