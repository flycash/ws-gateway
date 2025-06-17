package ioc

import (
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/gotomicro/ego/core/econf"
)

func InitExponentialBackOff() *backoff.ExponentialBackOff {
	type BackoffConfig struct {
		InitialInterval time.Duration `yaml:"initialInterval"`
		MaxInterval     time.Duration `yaml:"maxInterval"`
	}
	var cfg BackoffConfig
	err := econf.UnmarshalKey("server.websocket.backoff", &cfg)
	if err != nil {
		panic(err)
	}
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = cfg.InitialInterval
	b.MaxInterval = cfg.MaxInterval
	return b
}
