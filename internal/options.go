package internal

import (
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/limiter"
	"github.com/cenkalti/backoff/v5"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/elog"
)

// Option overrides a Container's default configuration.
type Option func(c *Container)

// WithServerOption inject server option to grpc server
// Session should not inject interceptor option, which is recommend by WithStreamInterceptor
// and WithUnaryInterceptor
// func WithServerOption(options ...grpc.ServerOption) Option {
// 	return func(c *Container) {
// 		if c.config.serverOptions == nil {
// 			c.config.serverOptions = make([]grpc.ServerOption, 0)
// 		}
// 		c.config.serverOptions = append(c.config.serverOptions, options...)
// 	}
// }

// WithNetwork inject network
func WithNetwork(network string) Option {
	return func(c *Container) {
		c.config.Network = network
	}
}

// WithLogger inject logger
func WithLogger(logger *elog.Component) Option {
	return func(c *Container) {
		c.logger = logger
	}
}

// WithUpgrader 设置升级器
func WithUpgrader(upgrader gateway.Upgrader) Option {
	return func(c *Container) {
		c.upgrader = upgrader
	}
}

// WithLinkEventHandler 设置连接事件处理器
func WithLinkEventHandler(linkEventHandler gateway.LinkEventHandler) Option {
	return func(c *Container) {
		c.linkEventHandler = linkEventHandler
	}
}

func WithCache(cache ecache.Cache) Option {
	return func(c *Container) {
		c.cache = cache
	}
}

func WithMQ(q mq.MQ, partitions int, topic string) Option {
	return func(c *Container) {
		c.mq = q
		c.mqPartitions = partitions
		c.mqTopic = topic
	}
}

func WithAutoCloseIdleLink(idleTimeout, idleScanInterval time.Duration) Option {
	return func(c *Container) {
		c.idleTimeout = idleTimeout
		c.idleScanInterval = idleScanInterval
	}
}

// WithLinkManager 设置连接管理器
func WithLinkManager(manager gateway.LinkManager) Option {
	return func(c *Container) {
		c.linkManager = manager
	}
}

// WithServiceRegistry 设置服务注册中心
func WithServiceRegistry(registry gateway.ServiceRegistry, updateNodeStateInterval time.Duration) Option {
	return func(c *Container) {
		c.registry = registry
		c.updateNodeStateInterval = updateNodeStateInterval
	}
}

// WithNodeInfo 设置节点信息
func WithNodeInfo(nodeInfo *apiv1.Node) Option {
	return func(c *Container) {
		c.nodeInfo = nodeInfo
	}
}

func WithTokenLimiter(tokenLimiter *limiter.TokenLimiter) Option {
	return func(c *Container) {
		c.tokenLimiter = tokenLimiter
	}
}

func WithExponentialBackOff(backoff *backoff.ExponentialBackOff) Option {
	return func(c *Container) {
		c.backoff = backoff
	}
}
