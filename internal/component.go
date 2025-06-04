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
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/ekit/syncx"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/constant"
	"github.com/gotomicro/ego/core/econf"
	"github.com/gotomicro/ego/core/elog"
	"github.com/gotomicro/ego/server"
)

var (
	_                  gateway.Server = &Component{}
	ErrUnknownReceiver                = errors.New("未知接收者")
)

type Component struct {
	name   string
	config *Config

	upgrader         gateway.Upgrader
	linkEventHandler gateway.LinkEventHandler
	localCache       ecache.Cache

	ctx           context.Context
	ctxCancelFunc context.CancelFunc

	messageQueue mq.MQ

	links  *syncx.Map[string, gateway.Link]
	logger *elog.Component
}

func newComponent(c *Container) *Component {
	ctx, cancelFunc := context.WithCancel(context.Background())
	return &Component{
		name:             c.name,
		config:           c.config,
		upgrader:         c.upgrader,
		linkEventHandler: c.linkEventHandler,
		localCache:       c.cache,
		ctx:              ctx,
		ctxCancelFunc:    cancelFunc,
		messageQueue:     c.messageQueue,
		links:            &syncx.Map[string, gateway.Link]{},
		logger:           c.logger,
	}
}

func (s *Component) Name() string {
	return fmt.Sprintf("%s.server", s.name)
}

func (s *Component) PackageName() string {
	return "ws-gateway"
}

func (s *Component) Init() error {
	// server的init操作有一些listen，必须先执行，否则有些通信，会有问题
	// todo: 微服务模式下需要ping各个依赖
	return nil
}

func (s *Component) Start() error {
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

func (s *Component) initConsumers() error {
	partitions := econf.GetInt("mq.kafka.push_message_topic_partitions")
	for i := 0; i < partitions; i++ {
		partition := i
		consumer, err := s.messageQueue.Consumer(econf.GetString("mq.kafka.push_message_topic"), s.Name())
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

func (s *Component) acceptConn(l net.Listener) {
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

func (s *Component) handleConn(conn net.Conn) {
	defer func() {
		err := conn.Close()
		if err != nil {
			s.logger.Error("关闭Conn失败",
				elog.String("step", "handleConn"),
				elog.FieldErr(err),
			)
		}
	}()

	sess, err := s.upgrader.Upgrade(conn)
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
	lk := link.New(s.ctx, linkID, sess.BizID, sess.UserID, conn)
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
			// 优雅关闭link
			if err1 := s.linkEventHandler.OnDisconnect(lk); err1 != nil {
				s.logger.Error("处理断链事件失败",
					elog.String("step", "handleConn"),
					elog.FieldErr(err1))
			}
			return
		}
	}
}

func (s *Component) getLinkID(bizID, userID int64) string {
	return fmt.Sprintf("%d-%d", bizID, userID)
}

func (s *Component) cacheSessionInfo(session gateway.Session) error {
	key := consts.SessionCacheKey(session)
	err := s.localCache.Set(context.Background(), key, session, 0)
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

func (s *Component) deleteSessionInfo(session gateway.Session) error {
	key := consts.SessionCacheKey(session)
	_, err := s.localCache.Delete(context.Background(), key)
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

func (s *Component) Stop() error {
	// todo: 强制关闭
	s.ctxCancelFunc()
	// 关闭消息队列客户端
	return nil
}

func (s *Component) Prepare() error {
	// Prepare 用于一些准备数据
	// 因为在OrderServer中，也会有invoker操作，需要放这个里面执行，需要区分他和真正server的init操作
	return nil
}

func (s *Component) GracefulStop(_ context.Context) error {
	// todo: 优雅关闭
	s.ctxCancelFunc()
	// 关闭消息队列客户端
	return nil
}

func (s *Component) Info() *server.ServiceInfo {
	info := server.ApplyOptions(
		server.WithName(s.Name()),
		server.WithScheme("ws"),
		server.WithAddress(s.config.Address()),
		server.WithKind(constant.ServiceProvider),
	)
	info.Healthy = s.Health()
	return &info
}

func (s *Component) Health() bool {
	return s.ctx.Err() == nil
}

func (s *Component) Invoker(_ ...func() error) {
}

func (s *Component) pushHandler(partition int, mqChan <-chan *mq.Message) {
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

func (s *Component) findLink(msg *apiv1.PushMessage) (gateway.Link, error) {
	linkID := s.getLinkID(msg.GetBizId(), msg.GetReceiverId())
	lk, ok := s.links.Load(linkID)
	if !ok {
		return nil, fmt.Errorf("%w: bizID=%d, userID=%d", ErrUnknownReceiver, msg.GetBizId(), msg.GetReceiverId())
	}
	return lk, nil
}
