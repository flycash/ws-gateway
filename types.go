package gateway

import (
	"net"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/gotomicro/ego/server"
)

type Server interface {
	server.OrderServer
}

// Link 表示一个用户连接
type Link interface {
	ID() string
	UID() int64
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

// Session 表示Websocket连接的会话信息
type Session struct {
	BizID  int64
	UserID int64
}

type Upgrader interface {
	Name() string
	Upgrade(conn net.Conn) (Session, error)
}
