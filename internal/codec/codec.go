package codec

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Codec 定义了用于编解码消息的接口
type Codec interface {
	Name() string
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}

type protoCodec struct{}

func NewProtoCodec() Codec {
	return &protoCodec{}
}

func (p *protoCodec) Name() string {
	return "proto"
}

func (p *protoCodec) Marshal(v any) ([]byte, error) {
	vv, ok := v.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("序列化失败，消息类型为 %T，期望类型为 proto.Message", v)
	}
	return proto.Marshal(vv)
}

func (p *protoCodec) Unmarshal(data []byte, v any) error {
	vv, ok := v.(proto.Message)
	if !ok {
		return fmt.Errorf("反序列化失败，消息类型为 %T，期望类型为 proto.Message", v)
	}
	return proto.Unmarshal(data, vv)
}

type jsonCodec struct{}

func NewJSONCodec() Codec {
	return &jsonCodec{}
}

func (j *jsonCodec) Name() string {
	return "json"
}

func (j *jsonCodec) Marshal(v any) ([]byte, error) {
	vv, ok := v.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("序列化失败, 消息类型为 %T, 期望类型为 proto.Message", v)
	}
	return protojson.Marshal(vv)
}

func (j *jsonCodec) Unmarshal(data []byte, v any) error {
	vv, ok := v.(proto.Message)
	if !ok {
		return fmt.Errorf("反序列化失败，消息类型为 %T，期望类型为 proto.Message", v)
	}
	return protojson.Unmarshal(data, vv)
}
