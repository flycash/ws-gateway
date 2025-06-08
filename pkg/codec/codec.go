package codec

import (
	"encoding/json"
	"fmt"
	"strconv"

	gatewayapiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
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

type IgnoreAnyProtoFieldJSONCodec struct {
	Codec
}

func NewIgnoreAnyProtoFieldJSONCodec() Codec {
	return &IgnoreAnyProtoFieldJSONCodec{Codec: NewJSONCodec()}
}

func (t *IgnoreAnyProtoFieldJSONCodec) Unmarshal(data []byte, v any) error {
	// 针对Message类型，特殊处理body字段
	if msg, ok := v.(*gatewayapiv1.Message); ok {
		// 先解析除了body以外的字段
		var temp struct {
			Cmd   string          `json:"cmd"`
			BizID string          `json:"bizId"`
			Key   string          `json:"key"`
			Body  json.RawMessage `json:"body"` // 保持原始JSON
		}

		if err := json.Unmarshal(data, &temp); err != nil {
			return err
		}

		msg.Cmd = gatewayapiv1.Message_CommandType(gatewayapiv1.Message_CommandType_value[temp.Cmd])
		bizID, err := strconv.ParseInt(temp.BizID, 10, 64)
		if err != nil {
			return fmt.Errorf("非法的BizID=%s", temp.BizID)
		}
		msg.BizId = bizID
		msg.Key = temp.Key

		// body字段保存为字节数组，不解析
		bodyBytes, _ := json.Marshal(temp.Body)
		msg.Body = &anypb.Any{
			TypeUrl: "application/json", // 标记为JSON类型
			Value:   bodyBytes,
		}

		return nil
	}

	return t.Codec.Unmarshal(data, v)
}
