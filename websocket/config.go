package websocket

import (
	"fmt"

	"github.com/gotomicro/ego/core/eflag"
)

const (
	defaultPort = 9002
)

// Config 用于记录websocket网关配置信息
type Config struct {
	Host              string // IP地址，默认0.0.0.0
	Port              int    // Port端口，默认9002
	Network           string // 网络类型，默认tcp4
	EnableLocalMainIP bool   // 自动获取ip地址
}

// DefaultConfig represents default config
// User should construct config base on DefaultConfig
func DefaultConfig() *Config {
	return &Config{
		Network: "tcp4",
		Host:    eflag.String("host"),
		Port:    defaultPort,
	}
}

// Address 返回websocket网关禁停地址
func (config Config) Address() string {
	// 如果是unix，那么启动方式为unix domain socket，host填写file
	if config.Network == "unix" {
		return config.Host
	}
	return fmt.Sprintf("%s:%d", config.Host, config.Port)
}
