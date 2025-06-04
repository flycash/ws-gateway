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
	"gitee.com/flycash/ws-gateway/internal/id"
	"github.com/gotomicro/ego/core/elog"
	"google.golang.org/protobuf/types/known/anypb"
)

var (
	_                                    gateway.LinkEventHandler = &linkEventHandler{}
	ErrUnKnownFrontendMessageFormat                               = errors.New("非法的网关API消息格式")
	ErrUnKnownFrontendMessageCommandType                          = errors.New("非法的网关API消息Cmd类型")
	ErrUnKnownFrontendMessageBodyType                             = errors.New("非法的网关API消息Body类型")
	ErrUnKnownBackendMessageFormat                                = errors.New("非法的msg服务消息格式")
	ErrUnKnownBackendMessageType                                  = errors.New("非法的msg服务消息Type类型")
	ErrUnKnownBackendMessagePushID                                = errors.New("非法的msg服务消息PushId")
	ErrUnKnownBackendMessageBodyType                              = errors.New("非法的msg服务消息Body类型")
)

type linkEventHandler struct {
	idGenerator                     id.Generator
	messageServiceClient            msgv1.MessageServiceClient
	codecHelper                     codec.Codec
	onFrontendSendMessageHandleFunc map[apiv1.Message_CommandType]func(lk gateway.Link, apiMessage *apiv1.Message) error
	onBackendPushMessageHandleFunc  map[apiv1.Message_Type]func(lk gateway.Link, message *apiv1.Message) error
	logger                          *elog.Component
	bizClients                      map[int64]apiv1.MessageServiceClient
}

// NewHandler 创建一个Link生命周期事件管理器
func NewHandler(idGenerator id.Generator, messageServiceClient msgv1.MessageServiceClient, codecHelper codec.Codec) gateway.LinkEventHandler {
	h := &linkEventHandler{
		idGenerator:                     idGenerator,
		messageServiceClient:            messageServiceClient,
		codecHelper:                     codecHelper,
		onFrontendSendMessageHandleFunc: make(map[apiv1.Message_CommandType]func(lk gateway.Link, apiMessage *apiv1.Message) error),
		onBackendPushMessageHandleFunc:  make(map[msgv1.Message_Type]func(lk gateway.Link, message *msgv1.Message) error),
		logger:                          elog.EgoLogger.With(elog.FieldComponent("LinkEventHandler")),
	}
	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_CHANNEL_MESSAGE_REQUEST] = h.handleOnMessageChannelMessageRequestCmd
	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_PUSH_CHANNEL_MESSAGE_RESPONSE] = h.handleOnMessagePushChannelMessageResponseCmd
	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_HEARTBEAT] = h.handleOnMessageHeartbeatCmd

	h.onBackendPushMessageHandleFunc[msgv1.Message_TYPE_CHANNEL_MESSAGE] = h.handleOnPushChannelMessage
	return h
}

func (l *linkEventHandler) OnConnect(lk gateway.Link) error {
	// 验证Auth包
	l.logger.Debug("Hello link = %s", elog.String("lid", lk.ID()))

	return nil
}

func (l *linkEventHandler) OnFrontendSendMessage(lk gateway.Link, payload []byte) error {
	l.logger.Debug("OnFrontendSendMessage link = %s", elog.String("linkID", lk.ID()))

	apiMessage := &apiv1.Message{}
	err := l.codecHelper.Unmarshal(payload, apiMessage)
	if err != nil {
		l.logger.Error("OnFrontendSendMessage",
			elog.String("step", "反序列化JSON消息失败"),
			elog.FieldErr(err),
		)
		return fmt.Errorf("%w", ErrUnKnownFrontendMessageFormat)
	}

	l.logger.Info("OnFrontendSendMessage",
		elog.String("step", "前端发送的消息(上行消息+对下行消息的响应)"),
		elog.String("消息体", apiMessage.String()))

	handleFunc, ok := l.onFrontendSendMessageHandleFunc[apiMessage.Cmd]
	if !ok {
		// 未知
		return fmt.Errorf("%w", ErrUnKnownFrontendMessageCommandType)
	}
	return handleFunc(lk, apiMessage)
}

func (l *linkEventHandler) OnDisconnect(lk gateway.Link) error {
	// 退出清理操作
	log.Printf("Goodbye link = %s!\n", lk.ID())
	return nil
}

func (l *linkEventHandler) handleOnMessageHeartbeatCmd(lk gateway.Link, apiMessage *apiv1.Message) error {
	// 心跳包原样返回
	return l.push(lk, apiMessage)
}

func (l *linkEventHandler) push(lk gateway.Link, message *apiv1.Message) error {
	payload, err := l.codecHelper.Marshal(message)
	if err != nil {
		l.logger.Error("push",
			elog.String("step", "序列化网关API消息失败"),
			elog.String("消息体", message.String()),
			elog.FieldErr(err))
		return err
	}
	err = lk.Send(payload)
	if err != nil {
		l.logger.Error("push",
			elog.String("step", "通过link对象下推网关API消息给用户"),
			elog.String("消息体", message.String()),
			elog.String("linkID", lk.ID()),
			elog.FieldErr(err))
		return err
	}
	return nil
}

func (l *linkEventHandler) handleOnMessageChannelMessageRequestCmd(lk gateway.Link, apiMessage *apiv1.Message) error {
	req := &apiv1.OnReceiveRequest{}
	err := apiMessage.Body.UnmarshalTo(req)
	if err != nil {
		l.logger.Error("OnFrontendSendMessage",
			elog.String("step", "handleOnMessageChannelMessageRequestCmd"),
			elog.String("step", "反序列化SendMessageRequest失败"),
			elog.FieldErr(err))
		return fmt.Errorf("%w", ErrUnKnownFrontendMessageBodyType)
	}

	l.logger.Debug("收到消息", elog.Any("msg", req))

	// 业务方管理
	msgID := l.idGenerator.Generate()
	sendTime := time.Now().UnixMilli()

	err = l.forwardToMessageService(lk, msgID, sendTime, req)
	if err != nil {
		l.logger.Error("OnFrontendSendMessage",
			elog.String("step", "handleOnMessageChannelMessageRequestCmd"),
			elog.String("step", "向消息服务转发消息失败"),
			elog.FieldErr(err))
		return fmt.Errorf("%w", err)
	}

	err = l.sendChannelMessageResponseToClient(lk, apiMessage.Seq, msgID, sendTime)
	if err != nil {
		l.logger.Error("OnFrontendSendMessage",
			elog.String("step", "handleOnMessageChannelMessageRequestCmd"),
			elog.String("step", "向前端返回响应失败"),
			elog.FieldErr(err))
		return fmt.Errorf("%w", err)
	}
	return nil
}

// func (l *linkEventHandler) forwardToBiz(lk gateway.Link,
//	bizID, msgID, sendTime int64,
//	req *gatewayapiv1.ChannelMessageRequest) error {
//	client := l.bizClients[bizID]
//	client.OnReceive(ctx...)
// }

func (l *linkEventHandler) forwardToMessageService(lk gateway.Link,
	msgID, sendTime int64,
	req *apiv1.OnReceiveRequest,
) error {
	// 记录发送时间
	channelMessage := &msgv1.ChannelMessage{
		Id:          msgID,
		SendId:      lk.UID(),
		Cid:         req.Msg.Cid,
		SendTime:    sendTime,
		ContentType: msgv1.ChannelMessage_ContentType(req.Msg.ContentType),
		Content:     req.Msg.Content,
	}
	body, _ := anypb.New(channelMessage)
	_, err := l.messageServiceClient.Send(context.Background(), &msgv1.SendRequest{
		Msg: &msgv1.Message{
			Type:   msgv1.Message_TYPE_CHANNEL_MESSAGE,
			PushId: lk.UID(),
			Body:   body,
		},
	})
	return err
}

func (l *linkEventHandler) sendChannelMessageResponseToClient(lk gateway.Link,
	seq string,
	msgID, sendTime int64,
) error {
	response := &apiv1.ChannelMessageResponse{
		Seq:      seq,
		MsgId:    msgID,
		SendTime: sendTime,
	}
	responseBody, _ := anypb.New(response)
	responseMessage := &apiv1.Message{
		Cmd:  apiv1.Message_COMMAND_TYPE_CHANNEL_MESSAGE_RESPONSE,
		Body: responseBody,
	}
	return l.push(lk, responseMessage)
}

func (l *linkEventHandler) OnBackendPushMessage(lk gateway.Link, message *msgv1.Message) error {
	if message == nil {
		return fmt.Errorf("%w", ErrUnKnownBackendMessageFormat)
	}
	if message.PushId <= 0 {
		return fmt.Errorf("%w", ErrUnKnownBackendMessagePushID)
	}
	handleFunc, ok := l.onBackendPushMessageHandleFunc[message.Type]
	if !ok {
		// 未知
		return fmt.Errorf("%w", ErrUnKnownBackendMessageType)
	}
	return handleFunc(lk, message)
}

func (l *linkEventHandler) handleOnPushChannelMessage(lk gateway.Link, message *msgv1.Message) error {
	channelMessage := &msgv1.ChannelMessage{}
	err := message.Body.UnmarshalTo(channelMessage)
	if err != nil {
		l.logger.Error("OnBackendPushMessage",
			elog.String("step", "handleOnPushChannelMessage"),
			elog.String("step", "反序列化msg服务消息体失败"),
			elog.FieldErr(err))
		return fmt.Errorf("%w", ErrUnKnownBackendMessageBodyType)
	}

	body, _ := anypb.New(&apiv1.PushChannelMessageRequest{
		MsgId: channelMessage.Id,
		Msg: &apiv1.ChannelMessage{
			Cid:         channelMessage.Cid,
			ContentType: apiv1.ChannelMessage_ContentType(channelMessage.ContentType),
			Content:     channelMessage.Content,
		},
		SendId:   channelMessage.SendId,
		SendTime: channelMessage.SendTime,
	})

	gatewayAPIMessage := &apiv1.Message{
		Cmd:  apiv1.Message_COMMAND_TYPE_PUSH_CHANNEL_MESSAGE_REQUEST,
		Body: body,
	}

	return l.push(lk, gatewayAPIMessage)
}

func (l *linkEventHandler) handleOnMessagePushChannelMessageResponseCmd(_ gateway.Link, message *apiv1.Message) error {
	resp := &apiv1.PushChannelMessageResponse{}
	err := message.Body.UnmarshalTo(resp)
	if err != nil {
		l.logger.Error("OnFrontendSendMessage",
			elog.String("step", "handleOnMessagePushChannelMessageResponseCmd"),
			elog.String("step", "反序列化PushChannelMessageResponse失败"),
			elog.FieldErr(err))
		return fmt.Errorf("%w", ErrUnKnownFrontendMessageBodyType)
	}
	// 使用resp.MsgId来充当Ack表明,推送的聊天消息ID == MsgId已成功被前端接收
	return nil
}
