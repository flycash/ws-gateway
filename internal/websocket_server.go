package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/consts"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/ekit/syncx"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/constant"
	"github.com/gotomicro/ego/core/elog"
	"github.com/gotomicro/ego/server"
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

	links  *syncx.Map[string, gateway.Link]
	logger *elog.Component
}

func newWebSocketServer(c *Container) *WebSocketServer {
	ctx, cancelFunc := context.WithCancel(context.Background())
	return &WebSocketServer{
		name:             c.name,
		config:           c.config,
		upgrader:         c.upgrader,
		linkEventHandler: c.linkEventHandler,
		cache:            c.cache,
		ctx:              ctx,
		ctxCancelFunc:    cancelFunc,
		mq:               c.mq,
		mqPartitions:     c.mqPartitions,
		mqTopic:          c.mqTopic,
		links:            &syncx.Map[string, gateway.Link]{},
		logger:           c.logger,
	}
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
	err := s.initConsumers()
	if err != nil {
		return err
	}

	l, err := net.Listen(s.config.Network, s.config.Address())
	if err != nil {
		return err
	}
	go s.acceptConn(l)

	<-s.ctx.Done()
	return l.Close()
}

func (s *WebSocketServer) initConsumers() error {
	for i := 0; i < s.mqPartitions; i++ {
		partition := i
		consumer, err := s.mq.Consumer(s.mqTopic, s.Name())
		if err != nil {
			s.logger.Error("获取MQ消费者失败",
				elog.String("step", "Start"),
				elog.String("step", "initConsumers"),
				elog.FieldErr(err),
			)
			return err
		}
		ch, err := consumer.ConsumeChan(context.Background())
		if err != nil {
			s.logger.Error("获取MQ消费者Chan失败",
				elog.String("step", "Start"),
				elog.String("step", "initConsumers"),
				elog.FieldErr(err),
			)
			return err
		}
		go s.pushHandler(partition, ch)
	}
	return nil
}

func (s *WebSocketServer) acceptConn(l net.Listener) {
	for {
		conn, err := l.Accept()
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
	if err1 := s.cacheSessionInfo(sess); err1 != nil {
		return
	}

	linkID := s.getLinkID(sess.BizID, sess.UserID)
	lk := link.New(s.ctx, linkID, sess, conn, link.WithCompression(compressionState))
	s.links.Store(linkID, lk)

	defer func() {
		if err1 := s.deleteSessionInfo(sess); err1 == nil {
			s.links.Delete(lk.ID())
		}
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

func (s *WebSocketServer) getLinkID(bizID, userID int64) string {
	return fmt.Sprintf("%d-%d", bizID, userID)
}

func (s *WebSocketServer) cacheSessionInfo(sess session.Session) error {
	key := consts.SessionCacheKey(sess)
	err := s.cache.Set(context.Background(), key, sess.String(), 0)
	if err != nil {
		s.logger.Error("记录Session失败",
			elog.String("step", "handleConn"),
			elog.String("step", "cacheSessionInfo"),
			elog.String("key", key),
			elog.FieldErr(err))
		return err
	}
	return nil
}

func (s *WebSocketServer) deleteSessionInfo(sess session.Session) error {
	key := consts.SessionCacheKey(sess)
	_, err := s.cache.Delete(context.Background(), key)
	if err != nil {
		s.logger.Error("删除Session失败",
			elog.String("step", "handleConn"),
			elog.String("step", "deleteSessionInfo"),
			elog.String("key", key),
			elog.FieldErr(err))
		return err
	}
	return nil
}

func (s *WebSocketServer) Stop() error {
	// todo: 强制关闭
	s.ctxCancelFunc()
	// 关闭消息队列客户端
	return nil
}

func (s *WebSocketServer) Prepare() error {
	// Prepare 用于一些准备数据
	// 因为在OrderServer中，也会有invoker操作，需要放这个里面执行，需要区分他和真正server的init操作
	return nil
}

func (s *WebSocketServer) GracefulStop(_ context.Context) error {
	// todo: 优雅关闭
	s.ctxCancelFunc()
	// 关闭消息队列客户端
	// 1. 停止接受新的websocket
	// 2. 遍历所有link，下发，redirect指令，重试3次，打印log

	return nil
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

func (s *WebSocketServer) pushHandler(partition int, mqChan <-chan *mq.Message) {
	s.logger.Info(s.Name(),
		elog.String("step", fmt.Sprintf("%s-%d", "pushHandler", partition)),
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
				elog.String("step", fmt.Sprintf("%s-%d", "pushHandler", partition)),
				elog.String("收到消息kafka消息", string(message.Value)))

			msg := &apiv1.PushMessage{}
			err := json.Unmarshal(message.Value, msg)
			if err != nil {
				s.logger.Error("反序列化MQ消息体失败",
					elog.String("step", "pushHandler"),
					elog.String("MQ消息体", string(message.Value)),
					elog.FieldErr(err),
				)
				continue
			}

			lk, err := s.findLink(msg)
			if err != nil {
				s.logger.Error("根据消息体查找Link失败",
					elog.String("step", "pushHandler"),
					elog.String("msg", msg.String()),
					elog.FieldErr(err))
				continue
			}

			err = s.linkEventHandler.OnBackendPushMessage(lk, msg)
			if err != nil {
				s.logger.Error("下推消息给前端用户失败",
					elog.String("step", "pushHandler"),
					elog.String("消息体", msg.String()),
					elog.FieldErr(err))
				continue
			}

		}
	}
}

func (s *WebSocketServer) findLink(msg *apiv1.PushMessage) (gateway.Link, error) {
	// 当前认为wsid+bizID+userID可以唯一确定一个link，复杂场景可能需要考虑多设备登录问题。
	linkID := s.getLinkID(msg.GetBizId(), msg.GetReceiverId())
	lk, ok := s.links.Load(linkID)
	if !ok {
		return nil, fmt.Errorf("%w: bizID=%d, userID=%d", ErrUnknownReceiver, msg.GetBizId(), msg.GetReceiverId())
	}
	return lk, nil
}
