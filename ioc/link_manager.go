package ioc

import (
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"github.com/gotomicro/ego/core/econf"
)

func InitLinkManager(c codec.Codec) gateway.LinkManager {
	type TimeoutConfig struct {
		Read  time.Duration `yaml:"read"`
		Write time.Duration `yaml:"write"`
	}
	type BufferConfig struct {
		ReceiveBufferSize int `yaml:"receiveBufferSize"`
		SendBufferSize    int `yaml:"sendBufferSize"`
	}
	type RetryStrategyConfig struct {
		InitInterval time.Duration `yaml:"initInterval"`
		MaxInterval  time.Duration `yaml:"maxInterval"`
		MaxRetries   int32         `yaml:"maxRetries"`
	}
	type Config struct {
		Timeout       TimeoutConfig       `yaml:"timeout"`
		BufferConfig  BufferConfig        `yaml:"buffer"`
		RetryStrategy RetryStrategyConfig `yaml:"retryStrategy"`
	}
	var cfg Config
	err := econf.UnmarshalKey("link", &cfg)
	if err != nil {
		panic(err)
	}
	return link.NewManager(
		c,
		&link.ManagerConfig{
			ReadTimeout:       cfg.Timeout.Read,
			WriteTimeout:      cfg.Timeout.Write,
			InitRetryInterval: cfg.RetryStrategy.InitInterval,
			MaxRetryInterval:  cfg.RetryStrategy.MaxInterval,
			MaxRetries:        cfg.RetryStrategy.MaxRetries,
			SendBufferSize:    cfg.BufferConfig.SendBufferSize,
			ReceiveBufferSize: cfg.BufferConfig.ReceiveBufferSize,
		})
}
