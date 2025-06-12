package gateway

import (
	"net"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"github.com/gotomicro/ego/server"
	"go.uber.org/multierr"
)

type Server interface {
	server.Server
}

// Link 表示一个用户连接
type Link interface {
	ID() string
	Session() session.Session
	Send(payload []byte) error
	Receive() <-chan []byte
	HasClosed() <-chan struct{}
	Close() error
}

// LinkEventHandler 表示 Link 的事件回调接口
type LinkEventHandler interface {
	// OnConnect 处理连接建立事件
	OnConnect(link Link) error

	// OnFrontendSendMessage 处理前端发送的消息,包含前端"主动"发送的消息(上行消息)以及前端对后端主动发送的消息(下行消息)的响应
	OnFrontendSendMessage(link Link, payload []byte) error

	// OnBackendPushMessage 处理后端下推/发送的消息,包含后端"主动"发送的消息(下行/下推消息)以及后端对前端主动发送的消息(上行消息)的响应
	OnBackendPushMessage(link Link, message *apiv1.PushMessage) error

	// OnDisconnect 处理连接断开事件
	OnDisconnect(link Link) error
}

type Upgrader interface {
	Name() string
	Upgrade(conn net.Conn) (session.Session, *compression.State, error)
}

type LinkEventHandlerWrapper struct {
	handlers []LinkEventHandler
}

func NewLinkEventHandlerWrapper(handler ...LinkEventHandler) *LinkEventHandlerWrapper {
	return &LinkEventHandlerWrapper{
		handlers: handler,
	}
}

func (l *LinkEventHandlerWrapper) OnConnect(lk Link) error {
	var err error
	for i := range l.handlers {
		err = multierr.Append(err, l.handlers[i].OnConnect(lk))
	}
	return err
}

func (l *LinkEventHandlerWrapper) OnFrontendSendMessage(lk Link, payload []byte) error {
	var err error
	for i := range l.handlers {
		err = multierr.Append(err, l.handlers[i].OnFrontendSendMessage(lk, payload))
	}
	return err
}

func (l *LinkEventHandlerWrapper) OnBackendPushMessage(lk Link, message *apiv1.PushMessage) error {
	var err error
	for i := range l.handlers {
		err = multierr.Append(err, l.handlers[i].OnBackendPushMessage(lk, message))
	}
	return err
}

func (l *LinkEventHandlerWrapper) OnDisconnect(lk Link) error {
	var err error
	for i := range l.handlers {
		err = multierr.Append(err, l.handlers[i].OnDisconnect(lk))
	}
	return err
}
