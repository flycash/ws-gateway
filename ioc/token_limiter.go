package ioc

import (
	"gitee.com/flycash/ws-gateway/internal/limiter"
	"github.com/gotomicro/ego/core/econf"
)

func InitTokenLimiter() *limiter.TokenLimiter {
	var cfg limiter.TokenLimiterConfig
	err := econf.UnmarshalKey("server.websocket.tokenLimiter", &cfg)
	if err != nil {
		panic(err)
	}
	t, err := limiter.NewTokenLimiter(cfg)
	if err != nil {
		panic(err)
	}
	return t
}
