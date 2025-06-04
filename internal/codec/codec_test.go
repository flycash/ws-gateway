//go:build unit

package codec_test

import (
	"testing"

	"gitee.com/flycash/ws-gateway/internal/codec"
	gatewayapiv1 "github.com/ecodeclub/ecodeim-gateway-api/gen/go/gatewayapi/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestJSONCodecTestSuite(t *testing.T) {
	t.Parallel()

	jsonCodec := codec.NewJSONCodec()
	assert.Equal(t, "json", jsonCodec.Name())

	suite.Run(t, &CodecSuite{c: jsonCodec})
}

func TestProtoCodecTestSuite(t *testing.T) {
	t.Parallel()

	protoCodec := codec.NewProtoCodec()
	assert.Equal(t, "proto", protoCodec.Name())

	suite.Run(t, &CodecSuite{c: protoCodec})
}

type CodecSuite struct {
	suite.Suite
	c codec.Codec
}

func (c *CodecSuite) TestMarshalAndUnmarshal() {
	tt := c.T()

	expectedSendMessageRequest := newExpectedSendMessageRequest()
	body, err := anypb.New(expectedSendMessageRequest)
	assert.NoError(tt, err)

	sendMsg := newMessage(gatewayapiv1.Message_COMMAND_TYPE_CHANNEL_MESSAGE_REQUEST, body)

	outgoingBytes, err := c.c.Marshal(sendMsg)
	assert.NoError(tt, err)

	tt.Logf("%s\n", string(outgoingBytes))

	receivedMsg := &gatewayapiv1.Message{}

	err = c.c.Unmarshal(outgoingBytes, receivedMsg)
	assert.NoError(tt, err)

	assert.Equal(tt, sendMsg.String(), receivedMsg.String())
	assert.True(tt, proto.Equal(sendMsg, receivedMsg))

	actualSendMessageRequest := &gatewayapiv1.ChannelMessageRequest{}
	err = receivedMsg.Body.UnmarshalTo(actualSendMessageRequest)
	assert.NoError(tt, err)

	assert.True(tt, proto.Equal(expectedSendMessageRequest, actualSendMessageRequest))
}

func (c *CodecSuite) TestHeartbeatMessage() {
	tt := c.T()
	tt.Helper()

	sendMsg := newMessage(gatewayapiv1.Message_COMMAND_TYPE_HEARTBEAT, nil)

	outgoingBytes, err := c.c.Marshal(sendMsg)
	assert.NoError(tt, err)

	receivedMsg := &gatewayapiv1.Message{}

	err = c.c.Unmarshal(outgoingBytes, receivedMsg)
	assert.NoError(tt, err)

	assert.Equal(tt, sendMsg.String(), receivedMsg.String())
	assert.True(tt, proto.Equal(sendMsg, receivedMsg))
}

func (c *CodecSuite) TestMarshalError() {
	msg := "invalid"
	payload, err := c.c.Marshal(msg)
	c.Error(err)
	c.Nil(payload)
}

func (c *CodecSuite) TestUnmarshalError() {
	msg := "invalid"
	c.Error(c.c.Unmarshal([]byte(msg), nil))
}

func newExpectedSendMessageRequest() *gatewayapiv1.ChannelMessageRequest {
	return &gatewayapiv1.ChannelMessageRequest{
		Msg: &gatewayapiv1.ChannelMessage{
			Cid:         2,
			ContentType: gatewayapiv1.ChannelMessage_CONTENT_TYPE_TEXT,
			Content:     "hello",
		},
	}
}

func newMessage(cmd gatewayapiv1.Message_CommandType, body *anypb.Any) *gatewayapiv1.Message {
	return &gatewayapiv1.Message{
		Cmd:  cmd,
		Body: body,
	}
}
