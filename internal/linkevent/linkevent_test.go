//go:build unit

package linkevent_test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	msgv1 "gitee.com/flycash/ws-gateway/api/proto/gen/msg/v1"
	"gitee.com/flycash/ws-gateway/internal/codec"
	"gitee.com/flycash/ws-gateway/internal/id"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/internal/linkevent"
	"gitee.com/flycash/ws-gateway/internal/linkevent/mocks"
	gatewayapiv1 "github.com/ecodeclub/ecodeim-gateway-api/gen/go/gatewayapi/v1"
	"github.com/gobwas/ws/wsutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestLinkEventHandlerWithJSONCodecTestSuite(t *testing.T) {
	t.Parallel()

	suite.Run(t, &LinkEventHandlerSuite{c: newJSONCodec()})
}

func TestLinkEventHandlerWithProtoCodecTestSuite(t *testing.T) {
	t.Parallel()

	suite.Run(t, &LinkEventHandlerSuite{c: newProtoCodec()})
}

type LinkEventHandlerSuite struct {
	suite.Suite
	c codec.Codec
}

func (s *LinkEventHandlerSuite) TestNew() {
	handler, ch := newLinkEventHandler(s.T(), s.c)
	s.NotNil(handler)
	s.NotNil(ch)
}

func (s *LinkEventHandlerSuite) TestOnConnect() {
	handler, _ := newLinkEventHandler(s.T(), s.c)

	serverConn, _ := newServerAndClientConn()
	lk := newLink("1", 1, serverConn)

	s.NoError(handler.OnConnect(lk))
}

func (s *LinkEventHandlerSuite) TestOnDisconnect() {
	handler, _ := newLinkEventHandler(s.T(), s.c)

	serverConn, _ := newServerAndClientConn()
	lk := newLink("2", 2, serverConn)

	s.NoError(handler.OnDisconnect(lk))
}

func (s *LinkEventHandlerSuite) TestOnFrontendSendMessage() {
	tt := s.T()

	tt.Parallel()

	tt.Run("应该返回错误, 当link已关闭", func(t *testing.T) {
		t.Parallel()

		handler, _ := newLinkEventHandler(t, s.c)
		serverConn, _ := newServerAndClientConn()
		lk := newLink("3", 3, serverConn)
		assert.NoError(t, lk.Close())
		<-lk.HasClosed()

		expectedBodyMessage := &gatewayapiv1.ChannelMessageRequest{
			Msg: &gatewayapiv1.ChannelMessage{
				Cid:         1,
				ContentType: gatewayapiv1.ChannelMessage_CONTENT_TYPE_TEXT,
				Content:     "Hello 船新IM",
			},
		}
		body, err := anypb.New(expectedBodyMessage)
		assert.NoError(t, err)

		apiMessage := &gatewayapiv1.Message{
			Cmd:  gatewayapiv1.Message_COMMAND_TYPE_CHANNEL_MESSAGE_REQUEST,
			Body: body,
		}
		payload, err := s.c.Marshal(apiMessage)
		assert.NoError(t, err)

		assert.ErrorIs(t, handler.OnFrontendSendMessage(lk, payload), link.ErrLinkClosed)
	})

	tt.Run("应该返回错误, 当前端发送的消息格式不符合规范", func(t *testing.T) {
		t.Parallel()

		handler, _ := newLinkEventHandler(t, s.c)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink("4", 4, serverConn)

		invalidAPIMessage := &gatewayapiv1.ChannelMessage{
			Cid:         1,
			ContentType: gatewayapiv1.ChannelMessage_CONTENT_TYPE_TEXT,
			Content:     "应该作为gatewayapiv1.Message的body发送",
		}

		clientErrorCh := make(chan error)
		go func() {
			payload, err := s.c.Marshal(invalidAPIMessage)
			if err != nil || len(payload) == 0 {
				panic(fmt.Sprintf("err = %s, payload = %s", err, string(payload)))
			}
			clientErrorCh <- wsutil.WriteClientBinary(clientConn, payload)
		}()

		assert.NoError(t, <-clientErrorCh)
		payload, ok := <-lk.Receive()
		assert.True(t, ok)
		assert.NotZero(t, payload)
		assert.ErrorIs(t, handler.OnFrontendSendMessage(lk, payload), linkevent.ErrUnKnownFrontendMessageFormat)
	})

	tt.Run("应该返回错误, 当前端发送的消息的Cmd非法时", func(t *testing.T) {
		t.Parallel()

		handler, _ := newLinkEventHandler(t, s.c)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink("5", 5, serverConn)

		apiMessage := &gatewayapiv1.Message{
			Cmd:  gatewayapiv1.Message_COMMAND_TYPE_INVALID_UNSPECIFIED,
			Body: &anypb.Any{},
		}

		clientErrorCh := make(chan error)
		go func() {
			payload, err := s.c.Marshal(apiMessage)
			if err != nil || len(payload) == 0 {
				panic(fmt.Sprintf("err = %s, payload = %s", err, string(payload)))
			}
			clientErrorCh <- wsutil.WriteClientBinary(clientConn, payload)
		}()

		assert.NoError(t, <-clientErrorCh)
		payload, ok := <-lk.Receive()
		assert.True(t, ok)
		assert.NotZero(t, payload)
		assert.ErrorIs(t, handler.OnFrontendSendMessage(lk, payload), linkevent.ErrUnKnownFrontendMessageCommandType)
	})

	tt.Run("应该返回错误, 当前端发送的消息的Body与Cmd不匹配时", func(t *testing.T) {
		t.Parallel()

		handler, _ := newLinkEventHandler(t, s.c)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink("6", 6, serverConn)

		bodyMessage := &gatewayapiv1.PushChannelMessageRequest{
			MsgId: 101,
		}
		body, err := anypb.New(bodyMessage)
		assert.NoError(t, err)
		apiMessage := &gatewayapiv1.Message{
			Cmd:  gatewayapiv1.Message_COMMAND_TYPE_CHANNEL_MESSAGE_REQUEST,
			Body: body,
		}

		clientErrorCh := make(chan error)
		go func() {
			payload, err := s.c.Marshal(apiMessage)
			if err != nil || len(payload) == 0 {
				panic(fmt.Sprintf("err = %s, payload = %s", err, string(payload)))
			}
			clientErrorCh <- wsutil.WriteClientBinary(clientConn, payload)
		}()

		assert.NoError(t, <-clientErrorCh)
		payload, ok := <-lk.Receive()
		assert.True(t, ok)
		assert.NotZero(t, payload)
		assert.ErrorIs(t, handler.OnFrontendSendMessage(lk, payload), linkevent.ErrUnKnownFrontendMessageBodyType)
	})

	tt.Run("应该返回心跳包, 当前端发送心跳消息", func(t *testing.T) {
		t.Parallel()

		handler, _ := newLinkEventHandler(t, s.c)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink("7", 7, serverConn)

		expectedHeartbeat := &gatewayapiv1.Message{
			Cmd:  gatewayapiv1.Message_COMMAND_TYPE_HEARTBEAT,
			Body: nil,
		}

		clientPayloadCh := make(chan []byte)
		clientErrorCh := make(chan error)
		go func() {
			payload, err := s.c.Marshal(expectedHeartbeat)
			t.Logf("heartbeat: %s\n", string(payload))
			if err != nil || len(payload) == 0 {
				panic(fmt.Sprintf("err = %s, payload = %s", err, string(payload)))
			}
			clientErrorCh <- wsutil.WriteClientBinary(clientConn, payload)
		}()
		go func() {
			payload, err := wsutil.ReadServerBinary(clientConn)
			clientErrorCh <- err
			clientPayloadCh <- payload
		}()

		payload, ok := <-lk.Receive()
		assert.True(t, ok)
		assert.NotZero(t, payload)
		assert.NoError(t, <-clientErrorCh)
		assert.NoError(t, handler.OnFrontendSendMessage(lk, payload))

		assert.NoError(t, <-clientErrorCh)
		payload = <-clientPayloadCh
		actualHeartbeat := &gatewayapiv1.Message{}
		assert.NoError(t, s.c.Unmarshal(payload, actualHeartbeat))
		assert.Equal(t, expectedHeartbeat.String(), actualHeartbeat.String())
	})

	tt.Run("应该转发聊天消息ChannelMessageRequest到下游消息服务, 当前端发送聊天消息", func(t *testing.T) {
		t.Parallel()

		handler, msgServiceChan := newLinkEventHandler(t, s.c)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink("8", 8, serverConn)

		expectedBodyMessage := &gatewayapiv1.ChannelMessageRequest{
			Msg: &gatewayapiv1.ChannelMessage{
				Cid:         1,
				ContentType: gatewayapiv1.ChannelMessage_CONTENT_TYPE_TEXT,
				Content:     "Hello 船新IM",
			},
		}
		body, err := anypb.New(expectedBodyMessage)
		assert.NoError(t, err)
		expectedAPIMessage := &gatewayapiv1.Message{
			Cmd:  gatewayapiv1.Message_COMMAND_TYPE_CHANNEL_MESSAGE_REQUEST,
			Body: body,
		}

		clientPayloadCh := make(chan []byte)
		clientErrorCh := make(chan error)
		go func() {
			// 模拟前端发送聊天消息
			payload, err2 := s.c.Marshal(expectedAPIMessage)
			t.Logf("ChannelMessageRequest: %s\n", string(payload))
			if err2 != nil || len(payload) == 0 {
				panic(fmt.Sprintf("err = %s, payload = %s", err2, string(payload)))
			}
			clientErrorCh <- wsutil.WriteClientBinary(clientConn, payload)
		}()
		go func() {
			// 模拟前端接收聊天消息响应
			payload, e := wsutil.ReadServerBinary(clientConn)
			clientErrorCh <- e
			clientPayloadCh <- payload
		}()

		// 处理聊天消息
		payload, ok := <-lk.Receive()
		assert.True(t, ok)
		assert.NotZero(t, payload)
		assert.NoError(t, <-clientErrorCh)
		assert.NoError(t, handler.OnFrontendSendMessage(lk, payload))

		// 验证msg服务是否接收到网关转发的聊天消息

		msg := assertMessageServiceReceivedMessage(t, lk, msgServiceChan, expectedBodyMessage)

		// 验证前端是否收到响应
		assertClientReceivedResponseMessage(t, msg, clientErrorCh, clientPayloadCh, s.c)
	})

	tt.Run("应该返回错误, 当前端发送的PushChannelMessageResponse非法", func(t *testing.T) {
		// 成功情况 详见OnPush子测试
		t.Skip()
		t.Parallel()
	})
}

func assertMessageServiceReceivedMessage(t *testing.T, lk gateway.Link, msgServiceChan <-chan *msgv1.SendRequest, expectedBodyMessage *gatewayapiv1.ChannelMessageRequest) *msgv1.ChannelMessage {
	t.Helper()

	req := <-msgServiceChan
	assert.Equal(t, lk.UID(), req.Msg.PushId)
	assert.Equal(t, msgv1.Message_TYPE_CHANNEL_MESSAGE, req.Msg.Type)
	msg := &msgv1.ChannelMessage{}
	assert.NoError(t, req.Msg.Body.UnmarshalTo(msg))
	assert.Equal(t, expectedBodyMessage.Msg.Cid, msg.Cid)
	// 类型不一致,但是数值应该一一对应
	assert.Equal(t, expectedBodyMessage.Msg.ContentType, gatewayapiv1.ChannelMessage_ContentType(msg.ContentType))
	assert.Equal(t, expectedBodyMessage.Msg.Content, msg.Content)
	assert.NotZero(t, msg.Id)
	assert.NotZero(t, msg.SendId)
	assert.Equal(t, lk.UID(), msg.SendId)
	assert.NotZero(t, msg.SendTime)
	return msg
}

func assertClientReceivedResponseMessage(t *testing.T, msg *msgv1.ChannelMessage, clientErrorCh chan error, clientPayloadCh chan []byte, protoCodec codec.Codec) {
	t.Helper()

	body, err := anypb.New(&gatewayapiv1.ChannelMessageResponse{
		MsgId:    msg.Id,
		SendTime: msg.SendTime,
	})
	assert.NoError(t, err)
	expectedGatewayResponse := &gatewayapiv1.Message{
		Cmd:  gatewayapiv1.Message_COMMAND_TYPE_CHANNEL_MESSAGE_RESPONSE,
		Body: body,
	}

	assert.NoError(t, <-clientErrorCh)
	payload := <-clientPayloadCh
	actualGatewayResponse := &gatewayapiv1.Message{}

	assert.NoError(t, protoCodec.Unmarshal(payload, actualGatewayResponse))
	assert.Equal(t, expectedGatewayResponse.String(), actualGatewayResponse.String())
}

func (s *LinkEventHandlerSuite) TestOnBackendPushMessage() {
	tt := s.T()

	tt.Parallel()

	tt.Run("应当返回错误, 当link已关闭", func(t *testing.T) {
		t.Parallel()

		handler, _ := newLinkEventHandler(t, s.c)
		serverConn, _ := newServerAndClientConn()
		lk := newLink("9", 9, serverConn)
		assert.NoError(t, lk.Close())
		<-lk.HasClosed()

		expectedBodyMessage := &msgv1.ChannelMessage{
			Id:          3,
			SendId:      10,
			Cid:         2,
			SendTime:    time.Now().UnixMilli(),
			ContentType: msgv1.ChannelMessage_CONTENT_TYPE_TEXT,
			Content:     "下推消息",
		}
		body, err := anypb.New(expectedBodyMessage)
		assert.NoError(t, err)

		expectedMessage := &msgv1.Message{
			Type:   msgv1.Message_TYPE_CHANNEL_MESSAGE,
			PushId: lk.UID(),
			Body:   body,
		}
		assert.ErrorIs(t, handler.OnBackendPushMessage(lk, expectedMessage), link.ErrLinkClosed)
	})

	tt.Run("应当返回错误, 当msg服务发送的消息格式不符合规范", func(t *testing.T) {
		t.Parallel()

		handler, _ := newLinkEventHandler(t, s.c)
		serverConn, _ := newServerAndClientConn()
		lk := newLink("10", 10, serverConn)

		message := (*msgv1.Message)(nil)
		assert.ErrorIs(t, handler.OnBackendPushMessage(lk, message), linkevent.ErrUnKnownBackendMessageFormat)
	})

	tt.Run("应当返回错误, 当msg服务发送的消息的Type非法时", func(t *testing.T) {
		t.Parallel()

		handler, _ := newLinkEventHandler(t, s.c)
		serverConn, _ := newServerAndClientConn()
		lk := newLink("11", 11, serverConn)

		message := &msgv1.Message{
			Type:   msgv1.Message_TYPE_INVALID_UNSPECIFIED,
			PushId: lk.UID(),
		}
		assert.Error(t, handler.OnBackendPushMessage(lk, message))

		message = &msgv1.Message{
			Type:   msgv1.Message_Type(1000),
			PushId: lk.UID(),
		}
		assert.ErrorIs(t, handler.OnBackendPushMessage(lk, message), linkevent.ErrUnKnownBackendMessageType)
	})

	tt.Run("应当返回错误, 当msg服务发送的消息的PushId非法时", func(t *testing.T) {
		t.Parallel()

		handler, _ := newLinkEventHandler(t, s.c)
		serverConn, _ := newServerAndClientConn()
		lk := newLink("12", 12, serverConn)

		bodyMessage := &msgv1.ChannelMessage{
			Id:          1,
			SendId:      2,
			Cid:         3,
			SendTime:    4,
			ContentType: msgv1.ChannelMessage_CONTENT_TYPE_TEXT,
			Content:     "PushId非法",
		}
		body, err := anypb.New(bodyMessage)
		assert.NoError(t, err)

		message := &msgv1.Message{
			Type:   msgv1.Message_TYPE_CHANNEL_MESSAGE,
			PushId: 0,
			Body:   body,
		}
		assert.ErrorIs(t, handler.OnBackendPushMessage(lk, message), linkevent.ErrUnKnownBackendMessagePushID)

		message = &msgv1.Message{
			Type:   msgv1.Message_TYPE_CHANNEL_MESSAGE,
			PushId: -1,
			Body:   body,
		}
		assert.ErrorIs(t, handler.OnBackendPushMessage(lk, message), linkevent.ErrUnKnownBackendMessagePushID)
	})

	tt.Run("应当返回错误, 当msg服务发送的消息的Body字段非法或与Type不匹配时", func(t *testing.T) {
		t.Parallel()

		handler, _ := newLinkEventHandler(t, s.c)
		serverConn, _ := newServerAndClientConn()
		lk := newLink("13", 13, serverConn)

		nilBodyMessage := &msgv1.Message{
			Type:   msgv1.Message_TYPE_CHANNEL_MESSAGE,
			PushId: lk.UID(),
			Body:   nil,
		}
		assert.ErrorIs(t, handler.OnBackendPushMessage(lk, nilBodyMessage), linkevent.ErrUnKnownBackendMessageBodyType)

		unknownBody := &gatewayapiv1.Message{}
		body, err := anypb.New(unknownBody)
		assert.NoError(t, err)

		invalidBodyMessage := &msgv1.Message{
			Type:   msgv1.Message_TYPE_CHANNEL_MESSAGE,
			PushId: lk.UID(),
			Body:   body,
		}
		assert.ErrorIs(t, handler.OnBackendPushMessage(lk, invalidBodyMessage), linkevent.ErrUnKnownBackendMessageBodyType)
	})

	tt.Run("前端应该返回响应PushChannelMessageResponse, 当网关发送下推消息请求", func(t *testing.T) {
		t.Parallel()

		handler, _ := newLinkEventHandler(t, s.c)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink("14", 14, serverConn)

		msgID := int64(1)
		bodyMessage := &gatewayapiv1.PushChannelMessageResponse{MsgId: msgID}
		body, _ := anypb.New(bodyMessage)
		expectedResponseAPIMessage := &gatewayapiv1.Message{
			Cmd:  gatewayapiv1.Message_COMMAND_TYPE_PUSH_CHANNEL_MESSAGE_RESPONSE,
			Body: body,
		}
		clientPayloadCh := make(chan []byte)
		clientErrorCh := make(chan error)
		go func() {
			// 模拟前端接收后端下推消息
			payload, e := wsutil.ReadServerBinary(clientConn)
			clientErrorCh <- e
			clientPayloadCh <- payload

			// 模拟前端返回下推消息的响应给后端
			payload, _ = s.c.Marshal(expectedResponseAPIMessage)
			clientErrorCh <- wsutil.WriteClientBinary(clientConn, payload)
		}()

		// 通过link下推消息
		expectedBodyMessage := &msgv1.ChannelMessage{
			Id:          msgID,
			SendId:      100,
			Cid:         2,
			SendTime:    time.Now().UnixMilli(),
			ContentType: msgv1.ChannelMessage_CONTENT_TYPE_TEXT,
			Content:     "下推消息",
		}
		body, err := anypb.New(expectedBodyMessage)
		assert.NoError(t, err)

		expectedMessage := &msgv1.Message{
			Type:   msgv1.Message_TYPE_CHANNEL_MESSAGE,
			PushId: lk.UID(),
			Body:   body,
		}
		assert.NoError(t, handler.OnBackendPushMessage(lk, expectedMessage))

		// 断言前端收到的下推消息
		assertClientReceivedPushMessage(t, clientErrorCh, clientPayloadCh, s.c, expectedBodyMessage)

		// 断言后端收到的下推消息的响应
		assert.NoError(t, <-clientErrorCh)
		payload, ok := <-lk.Receive()
		assert.True(t, ok)
		assert.NotZero(t, payload)
		assert.NoError(t, handler.OnFrontendSendMessage(lk, payload))
	})
}

func assertClientReceivedPushMessage(t *testing.T, clientErrorCh chan error, clientPayloadCh chan []byte, protoCodec codec.Codec, expectedBodyMessage *msgv1.ChannelMessage) {
	t.Helper()

	assert.NoError(t, <-clientErrorCh)
	pushPayload := <-clientPayloadCh

	actualMessage := &gatewayapiv1.Message{}
	assert.NoError(t, protoCodec.Unmarshal(pushPayload, actualMessage))

	actualBodyMessage := &gatewayapiv1.PushChannelMessageRequest{}
	assert.NoError(t, actualMessage.Body.UnmarshalTo(actualBodyMessage))
	assert.Equal(t, expectedBodyMessage.Id, actualBodyMessage.MsgId)
	assert.Equal(t, expectedBodyMessage.SendId, actualBodyMessage.SendId)
	assert.Equal(t, expectedBodyMessage.SendTime, actualBodyMessage.SendTime)
	assert.Equal(t, expectedBodyMessage.Cid, actualBodyMessage.Msg.Cid)
	// 仅类型不同,数值必须相同
	assert.Equal(t, expectedBodyMessage.ContentType, msgv1.ChannelMessage_ContentType(actualBodyMessage.Msg.ContentType))
	assert.Equal(t, expectedBodyMessage.Content, actualBodyMessage.Msg.Content)
}

func newLinkEventHandler(t *testing.T, c codec.Codec) (handler gateway.LinkEventHandler, msgRespChan <-chan *msgv1.SendRequest) {
	t.Helper()
	idGenerator, err := id.NewGenerator(1)
	require.NoError(t, err)
	client, ch := newMessageServiceLocalClient(t)

	return linkevent.NewHandler(idGenerator, client, c), ch
}

func newMessageServiceLocalClient(t *testing.T) (msgClient msgv1.MessageServiceClient, msgRespChan <-chan *msgv1.SendRequest) {
	t.Helper()
	reqChan := make(chan *msgv1.SendRequest, 1000)
	ctrl := gomock.NewController(t)
	msgServiceMock := mocks.NewMockMessageServiceServer(ctrl)
	msgServiceMock.EXPECT().Send(gomock.Any(), gomock.Any()).Do(func(ctx context.Context, in *msgv1.SendRequest) {
		reqChan <- in
	}).Return(nil, nil).AnyTimes()
	return msgv1.NewMessageServiceLocalClient(msgServiceMock), reqChan
}

func newProtoCodec() codec.Codec {
	return codec.NewProtoCodec()
}

func newJSONCodec() codec.Codec {
	return codec.NewJSONCodec()
}

func newLink(linkID string, uid int64, server net.Conn) gateway.Link {
	return link.New(linkID, uid, server)
}

func newServerAndClientConn() (server, client net.Conn) {
	return net.Pipe()
}
