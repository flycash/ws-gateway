package ioc

import (
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"github.com/gotomicro/ego/core/econf"
)

func InitSerializer() codec.Codec {
	configKey := "server.websocket"
	config := econf.GetStringMap(configKey)
	serializer, ok := config["serializer"].(string)
	if !ok {
		panic("server.websocket.serializer配置解析错误")
	}
	delete(config, "serializer")

	codecMapping := map[string]codec.Codec{
		"json":  codec.NewJSONCodec(),
		"proto": codec.NewProtoCodec(),
	}
	return codecMapping[serializer]
}
