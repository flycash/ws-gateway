package websocket

import (
	gateway "gitee.com/flycash/ws-gateway"
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

func WithMQ(q mq.MQ) Option {
	return func(c *Container) {
		c.messageQueue = q
	}
}
