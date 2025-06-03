package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	gateway "gitee.com/flycash/ws-gateway"
	msgv1 "gitee.com/flycash/ws-gateway/api/proto/gen/msg/v1"
	"gitee.com/flycash/ws-gateway/websocket/consts"
	"gitee.com/flycash/ws-gateway/websocket/link"
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/constant"
	"github.com/gotomicro/ego/core/econf"
	"github.com/gotomicro/ego/core/elog"
	"github.com/gotomicro/ego/server"
)

var (
	_                  gateway.Server = &Component{}
	ErrUnknownReceiver                = errors.New("未知接收者")
	ErrDuplicatedLink                 = errors.New("重复的连接")
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

	// todo: 分割成bucket将大锁变为小锁
	links       *sync.Map
	uidToLinkID *sync.Map
	logger      *elog.Component
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
		links:            new(sync.Map),
		uidToLinkID:      new(sync.Map),
		logger:           c.logger,
	}
}

func (s *Component) Name() string {
	return fmt.Sprintf("%s.server", s.name)
}

func (s *Component) PackageName() string {
	return "ecodeim/gateway/websocket"
}

func (s *Component) Init() error {
	// server的init操作有一些listen，必须先执行，否则有些通信，会有问题
	// todo: 微服务模式下需要ping各个依赖
	return nil
}

func (s *Component) Start() error {
	partitions := econf.GetInt("mq.kafka.channel_topic_partitions")
	for i := 0; i < partitions; i++ {
		partition := i
		consumer, err := s.messageQueue.Consumer(econf.GetString("mq.kafka.channel_topic"), s.Name())
		if err != nil {
			s.logger.Error(s.Name(),
				elog.String("step", "Start"),
				elog.String("step", "获取MQ消费者失败"),
				elog.FieldErr(err))
			return err
		}
		messageChan, err := consumer.ConsumeChan(context.Background())
		if err != nil {
			s.logger.Error(s.Name(),
				elog.String("step", "Start"),
				elog.String("step", "获取MQ消费者Chan失败"),
				elog.FieldErr(err))
			return err
		}
		go s.pushHandler(partition, messageChan)
	}

	l, err := net.Listen(s.config.Network, s.config.Address())
	if err != nil {
		return err
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				s.logger.Error("websocket", elog.String("step", "Accept"), elog.FieldErr(err))
				if errors.Is(err, net.ErrClosed) {
					return
				}
				var netOpErr *net.OpError
				if errors.As(err, &netOpErr) && (netOpErr.Timeout() || netOpErr.Temporary()) {
					continue
				}
			}
			go s.connHandler(conn)
		}
	}()

	<-s.ctx.Done()
	return l.Close()
}

func (s *Component) connHandler(conn net.Conn) {
	defer func() {
		err := conn.Close()
		if err != nil {
			s.logger.Error(s.Name(),
				elog.String("step", "connHandler"),
				elog.String("step", "Close Conn"),
				elog.FieldErr(err))
		}
	}()

	session, err := s.upgrader.Upgrade(conn)
	if err != nil {
		s.logger.Error(s.Name(),
			elog.String("step", "connHandler"),
			elog.String("step", "Upgrade"),
			elog.FieldErr(err))
		return
	}

	linkID, err := s.generateLinkID(session)
	if err != nil {
		s.logger.Error(s.Name(),
			elog.String("step", "connHandler"),
			elog.String("step", "generateLinkID"),
			elog.FieldErr(err))
		return
	}

	lk := link.New(linkID, session.UID, conn)
	err = s.addLinkCacheInfo(lk, session)
	if err != nil {
		return
	}

	defer func() {
		s.deleteLinkCacheInfo(lk, session)
		err := lk.Close()
		if err != nil {
			s.logger.Error(s.Name(),
				elog.String("step", "connHandler"),
				elog.String("step", "Close Link"),
				elog.FieldErr(err))
		}
	}()

	if err := s.linkEventHandler.OnConnect(lk); err != nil {
		// 记录日志
		s.logger.Error(s.Name(),
			elog.String("step", "connHandler"),
			elog.String("step", "OnConnect"),
			elog.FieldErr(err))
		return
	}

	for {
		select {
		// case <-time.After(): 在xxx时间内要么拿到数据包,要么拿到心跳包检查连接是否存活
		case message, ok := <-lk.Receive():
			if !ok {
				return
			}
			if err := s.linkEventHandler.OnFrontendSendMessage(lk, message); err != nil {
				// 记录日志
				s.logger.Error(s.Name(),
					elog.String("step", "connHandler"),
					elog.String("step", "OnFrontendSendMessage"),
					elog.FieldErr(err))
				// 根据错误类型来判定是否终止循环,然后优雅关闭连接
				if errors.Is(err, link.ErrLinkClosed) {
					return
				}
			}
		case <-lk.HasClosed():
			s.logger.Info(s.Name(),
				elog.String("step", "connHandler"), elog.String("link", "被关闭"))
			return
		case <-s.ctx.Done():
			s.logger.Info(s.Name(),
				elog.String("step", "connHandler"), elog.String("ctx", "被关闭"))
			// 优雅关闭link
			if err := s.linkEventHandler.OnDisconnect(lk); err != nil {
				// 记录日志
				s.logger.Error(s.Name(),
					elog.String("step", "connHandler"),
					elog.String("step", "OnDisconnect"),
					elog.FieldErr(err))
			}
			return
		}
	}
}

func (s *Component) generateLinkID(session gateway.Session) (string, error) {
	// todo: 生成link_id节点唯一即可, wsGateway_id + link_id即可找到连接
	//       当前只认为一个用户一个连接
	linkID := fmt.Sprintf("%d", session.UID)

	if _, ok := s.uidToLinkID.Load(session.UID); ok {
		return "", ErrDuplicatedLink
	}

	if _, ok := s.links.Load(linkID); ok {
		return "", ErrDuplicatedLink
	}

	return linkID, nil
}

func (s *Component) addLinkCacheInfo(lk gateway.Link, session gateway.Session) error {
	s.links.Store(lk.ID(), lk)
	s.uidToLinkID.Store(session.UID, lk.ID())

	uid := fmt.Sprintf("%d", session.UID)
	err := s.localCache.Set(context.Background(), uid, session, 0)
	if err != nil {
		s.logger.Error(s.Name(),
			elog.String("step", "connHandler"),
			elog.String("step", "addLinkCacheInfo"),
			elog.String("key", uid),
			elog.String("记录Session", "失败"), elog.FieldErr(err))
		return err
	}

	key := consts.UserWebSocketConnIDCacheKey(uid)
	err = s.localCache.Set(context.Background(), key, uid+lk.ID(), 0)
	if err != nil {
		s.logger.Error(s.Name(),
			elog.String("step", "connHandler"),
			elog.String("step", "deleteLinkCacheInfo"),
			elog.String("key", key),
			elog.String("记录websocket连接唯一标识信息", "失败"), elog.FieldErr(err))
		return err
	}
	return nil
}

func (s *Component) deleteLinkCacheInfo(lk gateway.Link, session gateway.Session) {
	s.links.Delete(lk.ID())
	s.uidToLinkID.Delete(session.UID)

	uid := fmt.Sprintf("%d", session.UID)
	n, err := s.localCache.Delete(context.Background(), uid)
	if n != 1 || err != nil {
		s.logger.Error(s.Name(),
			elog.String("step", "connHandler"),
			elog.String("step", "deleteLinkCacheInfo"),
			elog.String("key", uid),
			elog.String("删除Session", "失败"), elog.FieldErr(err))
	}

	key := consts.UserWebSocketConnIDCacheKey(uid)
	n, err = s.localCache.Delete(context.Background(), key)
	if n != 1 || err != nil {
		s.logger.Error(s.Name(),
			elog.String("step", "connHandler"),
			elog.String("step", "deleteLinkCacheInfo"),
			elog.String("key", key),
			elog.String("删除websocket连接唯一标识信息", "失败"), elog.FieldErr(err))
	}
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

func (s *Component) pushHandler(partition int, messageChan <-chan *mq.Message) {
	s.logger.Info(s.Name(),
		elog.String("step", fmt.Sprintf("%s-%d", "pushHandler", partition)),
		elog.String("step", "已启动"))

	for {
		select {
		case <-s.ctx.Done():
			return
		case mqMessage, ok := <-messageChan:
			if !ok {
				return
			}
			message := &msgv1.Message{}
			err := json.Unmarshal(mqMessage.Value, message)
			if err != nil {
				s.logger.Error(s.Name(),
					elog.String("step", "pushHandler"),
					elog.String("step", "从MQ的消息体中反序列化得到msg服务的消息体失败"),
					elog.String("MQ消息体", string(mqMessage.Value)),
					elog.FieldErr(err))
				continue
			}

			lk, err := s.findLink(message.PushId)
			if err != nil {
				s.logger.Error(s.Name(),
					elog.String("step", "pushHandler"),
					elog.String("step", "根据PushId查找Link对象"),
					elog.Int64("PushId", message.PushId),
					elog.FieldErr(err))
				continue
			}

			err = s.linkEventHandler.OnBackendPushMessage(lk, message)
			if err != nil {
				s.logger.Error(s.Name(),
					elog.String("step", "pushHandler"),
					elog.String("step", "下推消息给用户"),
					elog.String("消息体", message.String()),
					elog.FieldErr(err))
				continue
			}

		}
	}
}

func (s *Component) findLink(uid int64) (gateway.Link, error) {
	v, ok := s.uidToLinkID.Load(uid)
	if !ok {
		err := ErrUnknownReceiver
		s.logger.Error(s.Name(),
			elog.String("step", "pushHandler/push/findLink"),
			elog.String("step", "根据uid查找linkID"),
			elog.Int64("uid", uid),
			elog.FieldErr(err))
		return nil, err
	}
	linkID, _ := v.(string)

	v, ok = s.links.Load(linkID)
	if !ok {
		err := ErrUnknownReceiver
		s.logger.Error(s.Name(),
			elog.String("step", "findLink"),
			elog.String("step", "根据linkID查找Link对象"),
			elog.Int64("uid", uid),
			elog.String("linkID", linkID), elog.FieldErr(err))
		return nil, err
	}
	lk, _ := v.(gateway.Link)
	return lk, nil
}
