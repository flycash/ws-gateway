package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/event"
	"gitee.com/flycash/ws-gateway/internal/limiter"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"github.com/cenkalti/backoff/v5"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/constant"
	"github.com/gotomicro/ego/core/elog"
	"github.com/gotomicro/ego/server"
	clientv3 "go.etcd.io/etcd/client/v3"
)

var (
	_                  gateway.Server = &WebSocketServer{}
	ErrUnknownReceiver                = errors.New("未知接收者")
)

type WebSocketServer struct {
	name   string
	config *Config

	upgrader         gateway.Upgrader
	linkEventHandler gateway.LinkEventHandler
	cache            ecache.Cache

	ctx           context.Context
	ctxCancelFunc context.CancelFunc

	// 消费者
	consumers map[string]*event.Consumer

	listener net.Listener

	// 连接管理器
	linkManager gateway.LinkManager

	// 注册中心
	registry                gateway.ServiceRegistry
	updateNodeStateInterval time.Duration
	leaseID                 clientv3.LeaseID

	// 节点信息
	nodeInfo *apiv1.Node

	// 优雅关闭控制
	acceptingConnections atomic.Bool

	// 空闲管理
	idleTimeout      time.Duration
	idleScanInterval time.Duration

	// 灰度
	connLimiter *limiter.TokenLimiter
	backoff     *backoff.ExponentialBackOff

	logger *elog.Component
}

func newWebSocketServer(c *Container) *WebSocketServer {
	ctx, cancelFunc := context.WithCancel(context.Background())

	s := &WebSocketServer{
		name:                    c.name,
		config:                  c.config,
		upgrader:                c.upgrader,
		linkEventHandler:        c.linkEventHandler,
		cache:                   c.cache,
		ctx:                     ctx,
		ctxCancelFunc:           cancelFunc,
		consumers:               c.consumers,
		linkManager:             c.linkManager,
		registry:                c.registry,
		updateNodeStateInterval: c.updateNodeStateInterval,
		nodeInfo:                c.nodeInfo,
		idleTimeout:             c.idleTimeout,
		idleScanInterval:        c.idleScanInterval,
		connLimiter:             c.tokenLimiter,
		backoff:                 c.backoff,
		logger:                  c.logger,
	}

	// 初始状态接受连接
	s.acceptingConnections.Store(true)

	return s
}

func (s *WebSocketServer) Name() string {
	return s.name
}

func (s *WebSocketServer) PackageName() string {
	return "ws-gateway"
}

func (s *WebSocketServer) Init() error {
	// server的init操作有一些listen，必须先执行，否则有些通信，会有问题
	// todo: 微服务模式下需要ping各个依赖
	return nil
}

func (s *WebSocketServer) Start() error {
	// 开始监听网络连接
	l, err := net.Listen(s.config.Network, s.config.Address())
	if err != nil {
		return err
	}
	s.listener = l

	// 启动灰度发布的容量增长过程
	// 这个 goroutine 会在后台根据配置逐步增加令牌数量
	go s.connLimiter.StartRampUp(s.ctx)

	// 启动接受连接协程
	go s.acceptConn()

	// 启动空闲连接清理协程
	go s.cleanIdleLinks()

	// 初始化推送消息消费者
	for key := range s.consumers {
		switch key {
		case "pushMessage":
			err = s.consumers[key].Start(s.ctx, s.consumePushMessageEvent)
			if err != nil {
				return err
			}
		case "scaleUp":
			err = s.consumers[key].Start(s.ctx, s.consumeScaleUpEvent)
			if err != nil {
				return err
			}
		}
	}

	// 注册节点到服务注册中心
	leaseID, err := s.registry.Register(s.ctx, s.nodeInfo)
	if err != nil {
		s.logger.Error("节点注册到服务中心失败", elog.FieldErr(err))
		return fmt.Errorf("节点注册失败: %w", err)
	}

	s.leaseID = leaseID
	s.logger.Info("节点注册到服务中心成功",
		elog.String("nodeID", s.nodeInfo.GetId()),
		elog.Int64("leaseID", int64(leaseID)))

	// 启动租约续期
	go func() {
		if err1 := s.registry.KeepAlive(s.ctx, leaseID); err1 != nil {
			s.logger.Error("租约续期失败", elog.FieldErr(err1))
		}
	}()

	// 启动节点状态上报器
	go func() {
		updateFunc := func(node *apiv1.Node) bool {
			// 更新节点状态信息
			newLoad := s.linkManager.Len()
			if node.GetLoad() != newLoad {
				node.Load = newLoad
				return true // 需要更新
			}
			return false // 不需要更新
		}
		if err1 := s.registry.StartNodeStateUpdater(s.ctx, leaseID, s.nodeInfo.GetId(), updateFunc, s.updateNodeStateInterval); err1 != nil {
			s.logger.Error("节点状态更新器启动失败", elog.FieldErr(err1))
		}
	}()
	return nil
}

func (s *WebSocketServer) acceptConn() {
	for {
		// 检查是否还接受新连接
		if !s.acceptingConnections.Load() {
			s.logger.Info("不再接受新连接，正在优雅关闭中")
			return
		}

		// 在 Accept 前获取令牌
		if !s.connLimiter.Acquire() {
			backoffDuration := s.backoff.NextBackOff()
			s.logger.Warn("连接数已达当前容量上限，临时拒绝新连接",
				elog.Int64("currentCapacity", s.connLimiter.CurrentCapacity()),
				elog.Duration("backoff", backoffDuration))
			time.Sleep(backoffDuration)
			continue
		}

		//  获取令牌成功！立即重置退避策略，以便下次失败时从头开始计算。
		s.backoff.Reset()

		conn, err := s.listener.Accept()
		if err != nil {
			//  Accept 失败，归还刚刚获取的令牌
			s.connLimiter.Release()

			s.logger.Error("接受连接失败",
				elog.String("step", "acceptConn"),
				elog.FieldErr(err))
			if errors.Is(err, net.ErrClosed) {
				return
			}
			var netOpErr *net.OpError
			if errors.As(err, &netOpErr) && (netOpErr.Timeout() || netOpErr.Temporary()) {
				continue
			}

			// 确保循环继续，而不是意外退出
			continue
		}

		go s.handleConn(conn)
	}
}

func (s *WebSocketServer) handleConn(conn net.Conn) {
	// 归还令牌
	defer s.connLimiter.Release()

	defer func() {
		err := conn.Close()
		if err != nil && !errors.Is(err, net.ErrClosed) {
			s.logger.Warn("关闭Conn失败",
				elog.String("step", "handleConn"),
				elog.FieldErr(err),
			)
		}
	}()

	sess, compressionState, err := s.upgrader.Upgrade(conn)
	if err != nil {
		s.logger.Error("升级Conn失败",
			elog.String("step", "handleConn"),
			elog.FieldErr(err),
		)
		return
	}

	// 创建和管理连接
	lk, err := s.linkManager.NewLink(s.ctx, conn, sess, compressionState)
	if err != nil {
		s.logger.Error("创建Link失败",
			elog.String("step", "handleConn"),
			elog.FieldErr(err),
		)
		return
	}

	defer func() {
		s.linkManager.RemoveLink(lk.ID())
		if err1 := lk.Close(); err1 != nil {
			s.logger.Error("关闭Link失败",
				elog.String("step", "handleConn"),
				elog.FieldErr(err1),
			)
		}
	}()

	if err1 := s.linkEventHandler.OnConnect(lk); err1 != nil {
		s.logger.Error("处理连接事件失败",
			elog.String("step", "handleConn"),
			elog.FieldErr(err1),
		)
		return
	}

	defer func() {
		// 优雅关闭link
		if err1 := s.linkEventHandler.OnDisconnect(lk); err1 != nil {
			s.logger.Error("处理断连事件失败",
				elog.String("step", "handleConn"),
				elog.FieldErr(err1))
		}
	}()

	for {
		select {
		// case <-time.After(): 在xxx时间内要么拿到数据包,要么拿到心跳包检查连接是否存活
		case message, ok := <-lk.Receive():
			if !ok {
				return
			}
			if err1 := s.linkEventHandler.OnFrontendSendMessage(lk, message); err1 != nil {
				// 记录日志
				s.logger.Error("处理前端发送的消息失败",
					elog.String("step", "handleConn"),
					elog.FieldErr(err1),
				)
				// 根据错误类型来判定是否终止循环,然后优雅关闭连接
				if errors.Is(err1, link.ErrLinkClosed) {
					return
				}
			}
		case <-lk.HasClosed():
			s.logger.Info(
				s.Name(),
				elog.String("step", "handleConn"),
				elog.String("link", "被关闭"),
			)
			return
		case <-s.ctx.Done():
			s.logger.Info(s.Name(),
				elog.String("step", "handleConn"),
				elog.String("ctx", "被关闭"),
			)
			return
		}
	}
}

func (s *WebSocketServer) Stop() error {
	if err := s.connLimiter.Close(); err != nil {
		s.logger.Error("关闭连接限流器失败", elog.FieldErr(err))
	}
	s.ctxCancelFunc()
	return nil
}

func (s *WebSocketServer) Prepare() error {
	// Prepare 用于一些准备数据
	// 因为在OrderServer中，也会有invoker操作，需要放这个里面执行，需要区分他和真正server的init操作
	return nil
}

func (s *WebSocketServer) GracefulStop(ctx context.Context) error {
	s.logger.Info("开始优雅关闭服务器")

	// 停止接受新连接
	s.acceptingConnections.Store(false)
	_ = s.listener.Close()

	// 优雅注销节点（先降权重，等待，再删除）
	err := s.registry.GracefulDeregister(ctx, s.leaseID, s.nodeInfo.GetId())
	if err != nil {
		s.logger.Error("优雅注销节点失败", elog.FieldErr(err))
	} else {
		s.logger.Info("节点已从服务中心优雅注销")
	}

	// 关闭消费者
	for key := range s.consumers {
		err1 := s.consumers[key].Stop()
		if err1 != nil {
			s.logger.Error("关闭消费者失败",
				elog.String("name", s.consumers[key].Name()),
				elog.FieldErr(err1))
		} else {
			s.logger.Info("关闭消费者成功",
				elog.String("name", s.consumers[key].Name()),
			)
		}
	}

	// 获取其他可用节点
	availableNodes, err := s.registry.GetAvailableNodes(ctx, s.nodeInfo.GetId())
	if err != nil {
		s.logger.Error("获取可用节点失败", elog.FieldErr(err))
		// 即使获取失败，也继续优雅关闭流程
		availableNodes = &apiv1.NodeList{}
	}

	s.logger.Info("获取到可用节点",
		elog.Int("nodeCount", len(availableNodes.GetNodes())))

	// 优雅关闭所有连接（等待客户端主动断开或超时）
	err = s.linkManager.GracefulClose(ctx, availableNodes)
	if err != nil {
		s.logger.Warn("优雅关闭连接超时，将强制关闭", elog.FieldErr(err))
	} else {
		s.logger.Info("所有连接已优雅关闭")
	}

	<-ctx.Done()
	return s.Stop()
}

func (s *WebSocketServer) Info() *server.ServiceInfo {
	info := server.ApplyOptions(
		server.WithName(s.Name()),
		server.WithScheme("ws"),
		server.WithAddress(s.config.Address()),
		server.WithKind(constant.ServiceProvider),
	)
	info.Healthy = s.Health()
	return &info
}

func (s *WebSocketServer) Health() bool {
	return s.ctx.Err() == nil
}

func (s *WebSocketServer) Invoker(_ ...func() error) {
}

func (s *WebSocketServer) consumePushMessageEvent(_ context.Context, message *mq.Message) error {
	msg := &apiv1.PushMessage{}
	err := json.Unmarshal(message.Value, msg)
	if err != nil {
		s.logger.Error("反序列化MQ消息体失败",
			elog.String("step", "consumePushMessageEvent"),
			elog.String("MQ消息体", string(message.Value)),
			elog.FieldErr(err),
		)
		return err
	}

	lk, err := s.findLink(msg)
	if err != nil {
		s.logger.Error("根据消息体查找Link失败",
			elog.String("step", "consumePushMessageEvent"),
			elog.String("msg", msg.String()),
			elog.FieldErr(err))
		return err
	}

	err = s.linkEventHandler.OnBackendPushMessage(lk, msg)
	if err != nil {
		s.logger.Error("下推消息给前端用户失败",
			elog.String("step", "consumePushMessageEvent"),
			elog.String("消息体", msg.String()),
			elog.FieldErr(err))
		return err
	}
	return nil
}

func (s *WebSocketServer) findLink(msg *apiv1.PushMessage) (gateway.Link, error) {
	lk, ok := s.linkManager.FindLinkByUserInfo(session.UserInfo{
		BizID:  msg.GetBizId(),
		UserID: msg.GetReceiverId(),
	})
	if !ok {
		return nil, fmt.Errorf("%w: bizID=%d, userID=%d", ErrUnknownReceiver, msg.GetBizId(), msg.GetReceiverId())
	}
	return lk, nil
}

func (s *WebSocketServer) consumeScaleUpEvent(ctx context.Context, message *mq.Message) error {
	s.logger.Info("开始处理扩容再均衡事件")
	var msg event.ScaleUpEvent
	err := json.Unmarshal(message.Value, &msg)
	if err != nil {
		s.logger.Error("反序列化MQ消息体失败",
			elog.String("step", "consumeScaleUpEvent"),
			elog.String("MQ消息体", string(message.Value)),
			elog.FieldErr(err),
		)
		return err
	}

	// 新扩容节点忽略扩容事件
	for i := range msg.NewNodeList.Nodes {
		if msg.NewNodeList.Nodes[i].GetId() == s.nodeInfo.GetId() {
			s.logger.Info("新扩容节点忽略扩容事件",
				elog.String("step", "consumeScaleUpEvent"),
				elog.String("MQ消息体", string(message.Value)),
				elog.String("节点信息", s.nodeInfo.String()),
				elog.FieldErr(err),
			)
			return nil
		}
	}

	// 迁移超过 M * (N-k)/N 的连接，其中 M 是阈值，N 是最新节点数量，k 是扩容节点数量
	// 1000 - 1000 * (4-1)/4 = 1000 - 750 =  250
	// M - M * (N-k)/N  = M * K/N = 250
	// m := s.connLimiter.CurrentCapacity() // 生产使用，会增大到配置的Capacity，当前为10
	m := s.linkManager.Len() // 演示使用，用少量连接就可以看到迁移效果
	n := msg.TotalNodeCount
	k := int64(len(msg.NewNodeList.GetNodes()))
	count := m - ((m * (n - k)) / n)
	s.logger.Info("count := m * (k/n)",
		elog.Int64("count", count),
		elog.Int64("m", m),
		elog.Int64("k", k),
		elog.Int64("n", n))
	selector := link.NewRandomLinksSelector(count)
	err = s.linkManager.RedirectLinks(ctx, selector, msg.NewNodeList)
	if err != nil {
		s.logger.Error("扩容再均衡失败：下发重定向指令时出错", elog.FieldErr(err))
		return err
	}

	s.logger.Info("扩容再均衡成功",
		elog.String("step", "consumeScaleUpEvent"),
		elog.String("节点信息", s.nodeInfo.String()),
		elog.String("新增节点", msg.NewNodeList.String()),
		elog.Int64("重定向连接数", count),
	)
	return nil
}

func (s *WebSocketServer) cleanIdleLinks() {
	ticker := time.NewTicker(s.idleScanInterval)
	defer ticker.Stop()

	s.logger.Info("空闲连接清理器已启动",
		elog.Duration("scanInterval", s.idleScanInterval),
		elog.Duration("idleTimeout", s.idleTimeout))

	for {
		select {
		case <-s.ctx.Done():
			s.logger.Info("空闲连接清理器已停止")
			return
		case <-ticker.C:
			// 使用 LinkManager 的清理方法
			cleanedCount := s.linkManager.CleanIdleLinks(s.idleTimeout)
			if cleanedCount > 0 {
				s.logger.Info("清理了空闲连接", elog.Int("cleanedCount", cleanedCount))
			}
		}
	}
}
