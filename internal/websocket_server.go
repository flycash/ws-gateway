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
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/pkg/session"
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

	mq           mq.MQ
	mqPartitions int
	mqTopic      string

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
		mq:                      c.mq,
		mqPartitions:            c.mqPartitions,
		mqTopic:                 c.mqTopic,
		linkManager:             c.linkManager,
		registry:                c.registry,
		updateNodeStateInterval: c.updateNodeStateInterval,
		nodeInfo:                c.nodeInfo,
		idleTimeout:             c.idleTimeout,
		idleScanInterval:        c.idleScanInterval,
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
	// 1. 开始监听网络连接
	l, err := net.Listen(s.config.Network, s.config.Address())
	if err != nil {
		return err
	}
	s.listener = l

	// 2. 启动接受连接协程
	go s.acceptConn()

	// 3. 启动空闲连接清理协程
	go s.cleanIdleLinks()

	// 4. 初始化推送消息消费者
	err = s.initPushMessageConsumers()
	if err != nil {
		return fmt.Errorf("初始化下行消息消费者失败: %w", err)
	}

	// 5. 注册节点到服务注册中心
	leaseID, err := s.registry.Register(s.ctx, s.nodeInfo)
	if err != nil {
		s.logger.Error("节点注册到服务中心失败", elog.FieldErr(err))
		return fmt.Errorf("节点注册失败: %w", err)
	}

	s.leaseID = leaseID
	s.logger.Info("节点注册到服务中心成功",
		elog.String("nodeID", s.nodeInfo.GetId()),
		elog.Int64("leaseID", int64(leaseID)))

	// 6. 启动租约续期
	go func() {
		if err1 := s.registry.KeepAlive(s.ctx, leaseID); err1 != nil {
			s.logger.Error("租约续期失败", elog.FieldErr(err1))
		}
	}()

	// 7. 启动节点状态上报器
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

	<-s.ctx.Done()
	return nil
}

func (s *WebSocketServer) initPushMessageConsumers() error {
	for i := 0; i < s.mqPartitions; i++ {
		partition := i
		consumer, err := s.mq.Consumer(s.mqTopic, s.Name())
		if err != nil {
			s.logger.Error("获取MQ消费者失败",
				elog.String("step", "Start"),
				elog.String("step", "initPushMessageConsumers"),
				elog.FieldErr(err),
			)
			return err
		}
		ch, err := consumer.ConsumeChan(context.Background())
		if err != nil {
			s.logger.Error("获取MQ消费者Chan失败",
				elog.String("step", "Start"),
				elog.String("step", "initPushMessageConsumers"),
				elog.FieldErr(err),
			)
			return err
		}
		go s.pushMessageHandler(partition, ch)
	}
	return nil
}

func (s *WebSocketServer) acceptConn() {
	for {
		// 检查是否还接受新连接
		if !s.acceptingConnections.Load() {
			s.logger.Info("不再接受新连接，正在优雅关闭中")
			return
		}

		conn, err := s.listener.Accept()
		if err != nil {
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
		}

		go s.handleConn(conn)
	}
}

func (s *WebSocketServer) handleConn(conn net.Conn) {
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

	n := 3
	done := make(chan struct{}, n)
	// 停止接受新连接
	go func() {
		s.acceptingConnections.Store(false)
		_ = s.listener.Close()
		done <- struct{}{}
	}()

	ch := make(chan []*apiv1.Node)
	go func() {
		// 获取其他可用节点
		availableNodes, err := s.registry.GetAvailableNodes(ctx, s.nodeInfo.GetId())
		if err != nil {
			s.logger.Error("获取可用节点失败", elog.FieldErr(err))
			// 即使获取失败，也继续优雅关闭流程
			availableNodes = []*apiv1.Node{}
		}
		ch <- availableNodes
		s.logger.Info("获取到可用节点",
			elog.Int("nodeCount", len(availableNodes)))
		// 优雅注销节点（先降权重，等待，再删除）
		err = s.registry.GracefulDeregister(ctx, s.leaseID, s.nodeInfo.GetId())
		if err != nil {
			s.logger.Error("优雅注销节点失败", elog.FieldErr(err))
		} else {
			s.logger.Info("节点已从服务中心优雅注销")
		}
		done <- struct{}{}
	}()

	go func() {
		availableNodes := <-ch
		// 如果有可用节点，向所有连接发送重定向消息
		err := s.linkManager.RedirectLinks(ctx, link.NewAllLinksSelector(), &apiv1.NodeList{Nodes: availableNodes})
		if err != nil {
			s.logger.Error("发送重定向消息失败", elog.FieldErr(err))
		} else {
			s.logger.Info("向所有连接发送重定向消息成功")
		}
		// 优雅关闭所有连接（等待客户端主动断开或超时）
		err = s.linkManager.GracefulClose(ctx)
		if err != nil {
			s.logger.Warn("优雅关闭连接超时，将强制关闭", elog.FieldErr(err))
		} else {
			s.logger.Info("所有连接已优雅关闭")
		}
		done <- struct{}{}
	}()

	for {
		select {
		case <-ctx.Done():
			// 超时强制关闭
			return s.Stop()
		case <-done:
			n--
			if n == 0 {
				// 正常关闭
				return s.Stop()
			}
		}
	}
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

func (s *WebSocketServer) pushMessageHandler(partition int, mqChan <-chan *mq.Message) {
	s.logger.Info(s.Name(),
		elog.String("step", fmt.Sprintf("%s-%d", "pushMessageHandler", partition)),
		elog.String("step", "已启动"))

	for {
		select {
		case <-s.ctx.Done():
			return
		case message, ok := <-mqChan:
			if !ok {
				return
			}
			s.logger.Info(s.Name(),
				elog.String("step", fmt.Sprintf("%s-%d", "pushMessageHandler", partition)),
				elog.String("收到消息kafka消息", string(message.Value)))

			msg := &apiv1.PushMessage{}
			err := json.Unmarshal(message.Value, msg)
			if err != nil {
				s.logger.Error("反序列化MQ消息体失败",
					elog.String("step", "pushMessageHandler"),
					elog.String("MQ消息体", string(message.Value)),
					elog.FieldErr(err),
				)
				continue
			}

			lk, err := s.findLink(msg)
			if err != nil {
				s.logger.Error("根据消息体查找Link失败",
					elog.String("step", "pushMessageHandler"),
					elog.String("msg", msg.String()),
					elog.FieldErr(err))
				continue
			}

			err = s.linkEventHandler.OnBackendPushMessage(lk, msg)
			if err != nil {
				s.logger.Error("下推消息给前端用户失败",
					elog.String("step", "pushMessageHandler"),
					elog.String("消息体", msg.String()),
					elog.FieldErr(err))
				continue
			}

		}
	}
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
