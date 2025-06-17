package ioc

import (
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/internal/registry"
	"github.com/ego-component/eetcd"
	"github.com/gotomicro/ego/core/econf"
)

func InitRegistry(etcdClient *eetcd.Component) gateway.ServiceRegistry {
	type KeepAliveConfig struct {
		RetryInterval time.Duration `yaml:"retryInterval"`
		MaxRetries    int           `yaml:"maxRetries"`
	}
	type RetryStrategyConfig struct {
		InitInterval time.Duration `yaml:"initInterval"`
		MaxInterval  time.Duration `yaml:"maxInterval"`
		MaxRetries   int32         `yaml:"maxRetries"`
	}
	type Config struct {
		UpdateNodeStateInterval time.Duration       `yaml:"updateNodeStateInterval"`
		RetryStrategy           RetryStrategyConfig `yaml:"retryStrategy"`
		KeepAliveConfig         KeepAliveConfig     `yaml:"keepAlive"`
	}
	var cfg Config
	err := econf.UnmarshalKey("server.websocket.registry", &cfg)
	if err != nil {
		panic(err)
	}
	return registry.NewEtcdRegistry(
		etcdClient,
		cfg.RetryStrategy.InitInterval,
		cfg.RetryStrategy.MaxInterval,
		cfg.RetryStrategy.MaxRetries,
		cfg.KeepAliveConfig.RetryInterval,
		cfg.KeepAliveConfig.MaxRetries,
	)
}
