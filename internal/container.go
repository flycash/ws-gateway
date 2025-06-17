package internal

import (
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/econf"
	"github.com/gotomicro/ego/core/elog"
	"github.com/gotomicro/ego/core/util/xnet"
)

const (
	PackageName = "server.ws-gateway"
)

// Container 使用Builder模式构建一个Component实例
type Container struct {
	config           *Config
	name             string
	upgrader         gateway.Upgrader
	linkEventHandler gateway.LinkEventHandler
	cache            ecache.Cache
	mq               mq.MQ
	mqPartitions     int
	mqTopic          string

	// 连接管理器
	linkManager gateway.LinkManager

	// 注册中心
	registry                gateway.ServiceRegistry
	updateNodeStateInterval time.Duration

	// 节点信息
	nodeInfo *apiv1.Node

	// 空想管理
	idleTimeout      time.Duration
	idleScanInterval time.Duration

	logger *elog.Component
}

// DefaultContainer 返回暂存Component的默认配置信息的Container
func DefaultContainer() *Container {
	return &Container{
		config: DefaultConfig(),
		logger: elog.EgoLogger.With(elog.FieldComponent(PackageName)),
	}
}

// Load 从配置文件(比如: toml文件)中解析用户自定义的Component配置信息,并用用户自定义配置信息覆盖Container中暂存的Component的默认配置信息
// 后续Container会使用合并后的配置信息来构建一个Component实例
func Load(key string) *Container {
	c := DefaultContainer()
	c.logger = c.logger.With(elog.FieldComponentName(key))
	if err := econf.UnmarshalKey(key, &c.config); err != nil {
		c.logger.Panic("parse config error", elog.FieldErr(err), elog.FieldKey(key))
		return c
	}

	var (
		host string
		err  error
	)

	// 获取网卡ip
	if c.config.EnableLocalMainIP {
		host, _, err = xnet.GetLocalMainIP()
		if err != nil {
			elog.Error("get local main ip error", elog.FieldErr(err))
		} else {
			c.config.Host = host
		}
	}

	c.name = key
	return c
}

// Build 返回可用Component实例
func (c *Container) Build(options ...Option) *WebSocketServer {
	for _, option := range options {
		option(c)
	}
	return newWebSocketServer(c)
}
