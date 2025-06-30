package gateway

import (
	"context"
	"errors"
	"net"
	"time"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"github.com/gotomicro/ego/server"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/multierr"
)

var ErrRateLimitExceeded = errors.New("请求过于频繁，请稍后重试")

type Server interface {
	server.Server
}

// Link 表示一个抽象的用户连接，它封装了底层的网络连接（如 WebSocket、TCP），
// 并与一个用户会话 (Session) 绑定。它提供了面向业务的、统一的连接操作接口。
//
//go:generate mockgen -destination=./internal/mocks/gateway.mock.go -package=mocks -typed -source=./types.go
type Link interface {
	// ID 返回此连接的唯一标识符。
	ID() string

	// Session 返回与此连接绑定的用户会d话信息。
	Session() session.Session

	// Send 向客户端异步发送一条消息。
	// 如果发送失败（例如，缓冲区已满或连接已关闭），则返回错误。
	Send(payload []byte) error

	// Receive 返回一个只读通道，用于从客户端接收消息。
	// 调用方可以从该通道中持续读取客户端上行的数据。
	Receive() <-chan []byte

	// HasClosed 返回一个只读通道，该通道在连接被关闭时会关闭。
	// 这是一种非阻塞的、事件驱动的机制，用于监听连接的关闭事件。
	// 例如： `select { case <-link.HasClosed(): ... }`
	HasClosed() <-chan struct{}

	// Close 主动关闭此连接，并释放相关资源。
	Close() error

	// UpdateActiveTime 更新连接的最后活跃时间戳。
	// 通常在收到客户端消息或成功发送消息后调用，用于空闲连接检测。
	UpdateActiveTime()

	// TryCloseIfIdle 检查连接是否超过指定的空闲超时时间。
	// 如果已空闲超时，则关闭连接并返回 true；否则返回 false。
	TryCloseIfIdle(timeout time.Duration) bool
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

func (l *LinkEventHandlerWrapper) OnFrontendSendMessage(link Link, payload []byte) error {
	var err error
	for _, h := range l.handlers {
		err1 := h.OnFrontendSendMessage(link, payload)
		if errors.Is(err1, ErrRateLimitExceeded) {
			// 限流错误已经被处理（已通知前端），中断调用链即可
			return nil
		}
		err = multierr.Append(err, err1)
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

// LinkManager 定义了管理所有 Link 实例的接口。
// 它是网关节点的核心组件之一，负责连接的生命周期管理、查找和调度。
type LinkManager interface {
	// NewLink 基于底层的网络连接和用户会话，创建一个新的 Link 实例并纳入管理。
	NewLink(ctx context.Context, conn net.Conn, sess session.Session, compressionState *compression.State) (Link, error)

	// FindLinkByUserInfo 根据用户会话信息（如用户ID）查找对应的 Link 实例。
	FindLinkByUserInfo(userInfo session.UserInfo) (Link, bool)

	// RemoveLink 根据连接ID从管理器中移除一个 Link 实例。
	// 通常在 Link 关闭后被调用。
	RemoveLink(linkID string) bool

	// RedirectLinks 根据指定的 LinkSelector 策略，选择一组连接，并将它们重定向到其他可用节点。
	RedirectLinks(ctx context.Context, selector LinkSelector, availableNodes *apiv1.NodeList) error

	// PushMessage 根据指定的 LinkSelector 策略，选择一组连接，然后为它们推送 message
	PushMessage(ctx context.Context, selector LinkSelector, message *apiv1.Message) error

	// CleanIdleLinks 遍历所有连接，清理超过指定空闲时长的连接。
	// 返回被清理的连接数量。此方法通常由一个后台定时任务周期性调用。
	CleanIdleLinks(idleTimeout time.Duration) int

	// Len 返回当前管理的 Link 实例总数。
	Len() int64

	// Links 返回当前所有 Link 实例的快照切片。
	// 注意：这可能是一个耗时操作，应谨慎使用。
	Links() []Link

	// Close 立即关闭管理器以及其管理的所有连接，不进行任何优雅处理。
	Close() error

	// GracefulClose 以优雅的方式关闭管理器。
	// 它会首先尝试将所有连接重定向到其他节点，并在给定的超时时间内等待操作完成。
	GracefulClose(ctx context.Context, availableNodes *apiv1.NodeList) error
	// GracefulCloseV2 与GracefulClose的区别是外部拼好消息
	GracefulCloseV2(ctx context.Context, message *apiv1.Message) error
}

// LinkSelector 定义了如何从一组 Link 中挑选出子集的策略接口。
// 这是一个策略模式的应用，用于解耦"如何挑选"和"如何处理"。
type LinkSelector interface {
	// Select 根据特定策略，从输入的 links 切片中选择并返回一个子集。
	Select(links []Link) []Link
}

// ServiceRegistry 定义了服务注册与发现的标准接口。
type ServiceRegistry interface {
	// Register 将一个节点的信息注册到服务中心。
	// 它接收一个 apiv1.Node 对象，将其序列化为二进制格式后存入 Etcd。
	// 注册成功后会返回一个租约ID，用于后续的续租 (KeepAlive)。
	Register(ctx context.Context, node *apiv1.Node) (leaseID clientv3.LeaseID, err error)

	// KeepAlive 为指定的租约ID自动续期。
	// 这通常在一个独立的 goroutine 中运行，以保持节点在服务中心中的"存活"状态。
	KeepAlive(ctx context.Context, leaseID clientv3.LeaseID) error

	// Deregister 立即从服务中心注销一个节点。
	// 这会撤销节点的租约，使其键值对从 Etcd 中被删除。
	Deregister(ctx context.Context, leaseID clientv3.LeaseID, nodeID string) error

	// GracefulDeregister 优雅地注销节点：
	// 1. 先将节点权重设为0，停止接收新连接
	// 2. 等待一段时间让其他节点感知到变化
	// 3. 然后删除节点记录
	GracefulDeregister(ctx context.Context, leaseID clientv3.LeaseID, nodeID string) error

	// GetAvailableNodes 从服务中心获取除自身以外的所有可用节点列表。
	// 它会从 Etcd 中拉取所有节点信息，反序列化为 apiv1.Node 对象，并以切片形式返回。
	GetAvailableNodes(ctx context.Context, selfID string) (*apiv1.NodeList, error)

	// UpdateNodeInfo 更新 Etcd 中已注册节点的信息。
	// 这对于动态上报节点的负载 (Load) 等信息至关重要。
	// 此操作会重用节点注册时获取的租约ID。
	UpdateNodeInfo(ctx context.Context, leaseID clientv3.LeaseID, node *apiv1.Node) error

	// StartNodeStateUpdater 启动节点状态更新器，定期更新节点状态信息
	StartNodeStateUpdater(ctx context.Context, leaseID clientv3.LeaseID, nodeID string, updateFunc func(node *apiv1.Node) bool, interval time.Duration) error
}
