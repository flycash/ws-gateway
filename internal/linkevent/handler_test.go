//go:build unit

package linkevent_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/internal/linkevent"
	"gitee.com/flycash/ws-gateway/internal/linkevent/mocks"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"gitee.com/flycash/ws-gateway/pkg/encrypt"
	"gitee.com/flycash/ws-gateway/pkg/session"
	sessionmocks "gitee.com/flycash/ws-gateway/pkg/session/mocks"
	"github.com/ecodeclub/ecache/memory/lru"
	"github.com/ecodeclub/ekit/syncx"
	"github.com/gobwas/ws/wsutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
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
	handler := newLinkEventHandler(s.T(), s.c, 0, nil)
	s.NotNil(handler)
}

func (s *LinkEventHandlerSuite) TestOnConnect() {
	t := s.T()
	bizID := int64(1)
	handler := newLinkEventHandler(t, s.c, bizID, nil)

	serverConn, _ := newServerAndClientConn()
	lk := newLink(t.Context(), "1", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 123}), serverConn)

	s.NoError(handler.OnConnect(lk))
}

func (s *LinkEventHandlerSuite) TestOnDisconnect() {
	t := s.T()
	bizID := int64(2)
	handler := newLinkEventHandler(t, s.c, bizID, nil)

	serverConn, _ := newServerAndClientConn()
	lk := newLink(t.Context(), "2", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 223}), serverConn)

	s.NoError(handler.OnDisconnect(lk))
}

func (s *LinkEventHandlerSuite) TestOnFrontendSendMessage() {
	tt := s.T()

	tt.Parallel()

	tt.Run("应该返回错误, 当link已关闭", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		bizID := int64(3)

		client := mocks.NewMockBackendServiceClient(ctrl)
		client.EXPECT().OnReceive(gomock.Any(), gomock.Any()).Return(&apiv1.OnReceiveResponse{
			MsgId: bizID,
			BizId: bizID,
		}, nil).Times(1)

		handler := newLinkEventHandler(t, s.c, bizID, client)
		serverConn, _ := newServerAndClientConn()
		lk := newLink(t.Context(), "3", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 323}), serverConn)
		assert.NoError(t, lk.Close())
		<-lk.HasClosed()

		body, err := protojson.Marshal(&wrapperspb.StringValue{Value: "Hello 船新IM"})
		assert.NoError(t, err)

		apiMessage := &apiv1.Message{
			Cmd:  apiv1.Message_COMMAND_TYPE_UPSTREAM_MESSAGE,
			Key:  fmt.Sprintf("bizID-%d", bizID),
			Body: body,
		}
		payload, err := s.c.Marshal(apiMessage)
		assert.NoError(t, err)

		assert.ErrorIs(t, handler.OnFrontendSendMessage(lk, payload), link.ErrLinkClosed)
	})

	tt.Run("应该返回错误, 当前端发送的消息格式不符合规范", func(t *testing.T) {
		t.Parallel()

		bizID := int64(4)
		handler := newLinkEventHandler(t, s.c, bizID, nil)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink(t.Context(), "4", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 423}), serverConn)

		clientErrorCh := make(chan error)
		go func() {
			payload, err := s.c.Marshal(&wrapperspb.StringValue{Value: "应该作为gatewayapiv1.Message的body发送"})
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

		bizID := int64(5)
		handler := newLinkEventHandler(t, s.c, bizID, nil)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink(t.Context(), "5", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 523}), serverConn)

		apiMessage := &apiv1.Message{
			Cmd:  apiv1.Message_COMMAND_TYPE_INVALID_UNSPECIFIED,
			Key:  fmt.Sprintf("bizID-%d", bizID),
			Body: []byte{},
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

	tt.Run("应该返回心跳包, 当前端发送心跳消息", func(t *testing.T) {
		t.Parallel()

		bizID := int64(7)
		handler := newLinkEventHandler(t, s.c, bizID, nil)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink(t.Context(), "7", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 723}), serverConn)

		expectedHeartbeat := &apiv1.Message{
			Cmd:  apiv1.Message_COMMAND_TYPE_HEARTBEAT,
			Key:  fmt.Sprintf("bizID-%d", bizID),
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
		actualHeartbeat := &apiv1.Message{}
		assert.NoError(t, s.c.Unmarshal(payload, actualHeartbeat))
		assert.Equal(t, expectedHeartbeat.String(), actualHeartbeat.String())
	})

	tt.Run("应该转发业务消息到业务后端, 当前端发送业务消息", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		bizID := int64(8)

		client := mocks.NewMockBackendServiceClient(ctrl)
		client.EXPECT().OnReceive(gomock.Any(), gomock.Any()).Return(&apiv1.OnReceiveResponse{
			MsgId: bizID,
			BizId: bizID,
		}, nil).Times(1)

		handler := newLinkEventHandler(t, s.c, bizID, client)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink(t.Context(), "8", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 823}), serverConn)

		body, err := protojson.Marshal(&wrapperspb.StringValue{Value: "Hello 船新IM"})
		assert.NoError(t, err)
		expectedAPIMessage := &apiv1.Message{
			Cmd:  apiv1.Message_COMMAND_TYPE_UPSTREAM_MESSAGE,
			Key:  fmt.Sprintf("bizID-%d", bizID),
			Body: body,
		}

		clientPayloadCh := make(chan []byte)
		clientErrorCh := make(chan error)
		go func() {
			// 模拟前端发送聊天消息
			payload, err2 := s.c.Marshal(expectedAPIMessage)
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
	})

	tt.Run("应该返回错误，当转发业务消息到业务后端失败", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		bizID := int64(6)

		client := mocks.NewMockBackendServiceClient(ctrl)
		wantErr := errors.New("mock错误")
		client.EXPECT().OnReceive(gomock.Any(), gomock.Any()).Return(nil, wantErr).AnyTimes()

		handler := newLinkEventHandler(t, s.c, bizID, client)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink(t.Context(), "8", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 623}), serverConn)

		body, err := protojson.Marshal(&wrapperspb.StringValue{Value: "Hello 船新IM"})
		assert.NoError(t, err)
		expectedAPIMessage := &apiv1.Message{
			Cmd:  apiv1.Message_COMMAND_TYPE_UPSTREAM_MESSAGE,
			Key:  fmt.Sprintf("bizID-%d", bizID),
			Body: body,
		}

		clientPayloadCh := make(chan []byte)
		clientErrorCh := make(chan error)
		go func() {
			// 模拟前端发送聊天消息
			payload, err2 := s.c.Marshal(expectedAPIMessage)
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
		err = handler.OnFrontendSendMessage(lk, payload)
		assert.ErrorIs(t, err, wantErr)
		assert.ErrorIs(t, err, linkevent.ErrMaxRetriesExceeded)
	})
}

func (s *LinkEventHandlerSuite) TestOnBackendPushMessage() {
	tt := s.T()

	tt.Parallel()

	tt.Run("应当返回错误, 当link已关闭", func(t *testing.T) {
		t.Parallel()

		bizID := int64(9)
		handler := newLinkEventHandler(t, s.c, bizID, nil)
		serverConn, _ := newServerAndClientConn()
		lk := newLink(t.Context(), "9", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 923}), serverConn)
		assert.NoError(t, lk.Close())
		<-lk.HasClosed()

		body, err := protojson.Marshal(&wrapperspb.StringValue{Value: "下推消息"})
		assert.NoError(t, err)

		expectedMessage := &apiv1.PushMessage{
			Key:        fmt.Sprintf("bizID-%d", bizID),
			BizId:      bizID,
			ReceiverId: bizID,
			Body:       body,
		}
		assert.ErrorIs(t, handler.OnBackendPushMessage(lk, expectedMessage), link.ErrLinkClosed)
	})

	tt.Run("应当返回错误, 当业务后端发送的消息格式不符合规范", func(t *testing.T) {
		t.Parallel()

		bizID := int64(10)
		handler := newLinkEventHandler(t, s.c, bizID, nil)
		serverConn, _ := newServerAndClientConn()
		lk := newLink(t.Context(), "10", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 1023}), serverConn)

		message := (*apiv1.PushMessage)(nil)
		assert.ErrorIs(t, handler.OnBackendPushMessage(lk, message), linkevent.ErrUnKnownBackendMessageFormat)
	})

	tt.Run("前端应该返回响应, 当网关发送下推消息请求", func(t *testing.T) {
		t.Parallel()

		bizID := int64(11)
		handler := newLinkEventHandler(t, s.c, bizID, nil)
		serverConn, clientConn := newServerAndClientConn()
		lk := newLink(t.Context(), "11", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 1123}), serverConn)

		msgID := int64(1)
		body, _ := protojson.Marshal(&wrapperspb.Int64Value{Value: msgID})
		expectedResponseAPIMessage := &apiv1.Message{
			Cmd:  apiv1.Message_COMMAND_TYPE_DOWNSTREAM_ACK,
			Key:  fmt.Sprintf("bizID-%d", bizID),
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
		expectedMessage := &apiv1.PushMessage{
			Key:        fmt.Sprintf("bizID-%d", bizID),
			BizId:      bizID,
			ReceiverId: bizID,
			Body:       body,
		}
		assert.NoError(t, handler.OnBackendPushMessage(lk, expectedMessage))

		// 断言前端收到的下推消息
		assertClientReceivedPushMessage(t, clientErrorCh, clientPayloadCh, s.c, &apiv1.Message{
			Cmd:  apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
			Key:  fmt.Sprintf("bizID-%d", bizID),
			Body: body,
		})

		// 断言后端收到的下推消息的响应
		assert.NoError(t, <-clientErrorCh)
		payload, ok := <-lk.Receive()
		assert.True(t, ok)
		assert.NotZero(t, payload)
		assert.NoError(t, handler.OnFrontendSendMessage(lk, payload))
	})

	tt.Run("应该启动重传任务, 当发送下推消息", func(t *testing.T) {
		t.Parallel()

		bizID := int64(12)
		handler := newLinkEventHandler(t, s.c, bizID, nil)
		serverConn, _ := newServerAndClientConn()
		lk := newLink(t.Context(), "12", createTestSession(t.Context(), session.UserInfo{BizID: bizID, UserID: 1223}), serverConn)

		body, _ := protojson.Marshal(&wrapperspb.StringValue{Value: "测试重传"})
		pushMessage := &apiv1.PushMessage{
			Key:        "test-retry-key",
			BizId:      bizID,
			ReceiverId: bizID,
			Body:       body,
		}

		// 发送下推消息
		assert.NoError(t, handler.OnBackendPushMessage(lk, pushMessage))

		// 验证重传任务已启动（通过统计信息）
		// 这里我们无法直接访问pushRetryManager，但可以通过发送ACK来验证
		ackMessage := &apiv1.Message{
			Cmd:  apiv1.Message_COMMAND_TYPE_DOWNSTREAM_ACK,
			Key:  "test-retry-key",
			Body: body,
		}

		// 发送ACK消息应该能成功处理
		assert.NoError(t, handler.OnFrontendSendMessage(lk, s.marshalMessage(t, ackMessage)))
	})
}

func (s *LinkEventHandlerSuite) marshalMessage(t *testing.T, msg *apiv1.Message) []byte {
	payload, err := s.c.Marshal(msg)
	assert.NoError(t, err)
	return payload
}

func assertClientReceivedPushMessage(t *testing.T, clientErrorCh chan error, clientPayloadCh chan []byte, protoCodec codec.Codec, expectedBodyMessage *apiv1.Message) {
	t.Helper()
	assert.NoError(t, <-clientErrorCh)
	pushPayload := <-clientPayloadCh
	actualMessage := &apiv1.Message{}
	assert.NoError(t, protoCodec.Unmarshal(pushPayload, actualMessage))
	assert.True(t, proto.Equal(expectedBodyMessage, actualMessage), fmt.Sprintf("want = %s, got = %s", expectedBodyMessage.String(), actualMessage.String()))
}

func newLinkEventHandler(t *testing.T, c codec.Codec, bizID int64, client apiv1.BackendServiceClient) *linkevent.Handler {
	t.Helper()
	// 测试中使用none加密器（不加密）
	encryptor := encrypt.NewNoneEncryptor()

	return linkevent.NewHandler(lru.NewCache(10000), 3*time.Second, 10*time.Minute, c, encryptor, func() *syncx.Map[int64, apiv1.BackendServiceClient] {
		clients := &syncx.Map[int64, apiv1.BackendServiceClient]{}
		clients.Store(bizID, client)
		return clients
	}, time.Second*3, time.Second, 3*time.Second, 3, time.Minute, 3)
}

func newProtoCodec() codec.Codec {
	return codec.NewProtoCodec()
}

func newJSONCodec() codec.Codec {
	return codec.NewJSONCodec()
}

func newLink(ctx context.Context, linkID string, sess session.Session, server net.Conn) gateway.Link {
	return link.New(ctx, linkID, sess, server)
}

func newServerAndClientConn() (server, client net.Conn) {
	return net.Pipe()
}

// createTestSession 创建测试用的session
func createTestSession(ctx context.Context, userInfo session.UserInfo) session.Session {
	ctrl := gomock.NewController(&testing.T{})
	mockRedis := sessionmocks.NewMockCmdable(ctrl)

	// Mock session creation
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil)).AnyTimes()

	// Mock session destruction (Del operation) - 用于Link关闭时
	mockRedis.EXPECT().Del(gomock.Any(), gomock.Any()).
		Return(redis.NewIntResult(int64(1), nil)).AnyTimes()

	provider := session.NewRedisSessionBuilder(mockRedis)
	sess, _, _ := provider.Build(ctx, userInfo)

	return sess
}
