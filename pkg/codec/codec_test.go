//go:build unit

package codec_test

import (
	"testing"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
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

	expectedBody := wrapperspb.String("hello, world")
	body, err := protojson.Marshal(wrapperspb.String("hello, world"))
	assert.NoError(tt, err)

	sendMsg := &apiv1.Message{
		Key:  "biz-id-1-key",
		Cmd:  apiv1.Message_COMMAND_TYPE_UPSTREAM_MESSAGE,
		Body: body,
	}

	bytes, err := c.c.Marshal(sendMsg)
	assert.NoError(tt, err)

	receivedMsg := &apiv1.Message{}
	err = c.c.Unmarshal(bytes, receivedMsg)
	assert.NoError(tt, err)

	assert.Equal(tt, sendMsg.String(), receivedMsg.String())
	assert.True(tt, proto.Equal(sendMsg, receivedMsg))

	actualBody := &wrapperspb.StringValue{}
	err = protojson.Unmarshal(receivedMsg.GetBody(), actualBody)
	assert.NoError(tt, err)
	assert.True(tt, proto.Equal(expectedBody, actualBody))
}

func (c *CodecSuite) TestHeartbeatMessage() {
	t := c.T()
	sendMsg := &apiv1.Message{
		Key:  "biz-id-2-key",
		Cmd:  apiv1.Message_COMMAND_TYPE_HEARTBEAT,
		Body: nil,
	}

	bytes, err := c.c.Marshal(sendMsg)
	assert.NoError(t, err)

	receivedMsg := &apiv1.Message{}
	err = c.c.Unmarshal(bytes, receivedMsg)
	assert.NoError(t, err)

	assert.Equal(t, sendMsg.String(), receivedMsg.String())
	assert.True(t, proto.Equal(sendMsg, receivedMsg))
}

func (c *CodecSuite) TestMarshalError() {
	t := c.T()
	msg := "invalid"
	payload, err := c.c.Marshal(msg)
	assert.Error(t, err)
	assert.Nil(t, payload)
}

func (c *CodecSuite) TestUnmarshalError() {
	t := c.T()
	msg := "invalid"
	assert.Error(t, c.c.Unmarshal([]byte(msg), nil))
}
