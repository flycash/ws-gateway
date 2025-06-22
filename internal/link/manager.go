package link

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"github.com/ecodeclub/ekit/syncx"
	"github.com/gotomicro/ego/core/elog"
)

// 默认配置常量
const (
	DefaultReadTimeout       = 30 * time.Second
	DefaultWriteTimeout      = 10 * time.Second
	DefaultInitRetryInterval = 1 * time.Second
	DefaultMaxRetryInterval  = 5 * time.Second
	DefaultMaxRetries        = 3
	DefaultWriteBufferSize   = 256
	DefaultReadBufferSize    = 256
	DefaultCloseTimeout      = 1 * time.Second
	DefaultUserRateLimit     = 10
)

var _ gateway.LinkManager = (*Manager)(nil)

// ManagerConfig Manager 的配置
type ManagerConfig struct {
	// Link 创建时的默认配置
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	InitRetryInterval time.Duration
	MaxRetryInterval  time.Duration
	MaxRetries        int32
	SendBufferSize    int
	ReceiveBufferSize int
	UserRateLimit     int
}

// DefaultManagerConfig 返回默认配置

type Manager struct {
	links       *syncx.Map[string, *Link]
	config      *ManagerConfig
	len         atomic.Int64
	codecHelper codec.Codec
	logger      *elog.Component
}

// NewManager 创建一个新的 Manager 用于管理 Link的生命周期
func NewManager(codecHelper codec.Codec, config *ManagerConfig) *Manager {
	if config == nil {
		config = &ManagerConfig{
			ReadTimeout:       DefaultReadTimeout,
			WriteTimeout:      DefaultWriteTimeout,
			InitRetryInterval: DefaultInitRetryInterval,
			MaxRetryInterval:  DefaultMaxRetryInterval,
			MaxRetries:        DefaultMaxRetries,
			SendBufferSize:    DefaultWriteBufferSize,
			ReceiveBufferSize: DefaultReadBufferSize,
			UserRateLimit:     DefaultUserRateLimit,
		}
	}
	return &Manager{
		links:       &syncx.Map[string, *Link]{},
		codecHelper: codecHelper,
		config:      config,
		logger:      elog.EgoLogger.With(elog.FieldComponent("Link.Manager")),
	}
}

// NewLink 基于底层的网络连接和用户会话，创建一个新的 Link 实例并纳入管理。
// 这是所有新连接加入系统的入口点。
func (m *Manager) NewLink(ctx context.Context, conn net.Conn, sess session.Session, compressionState *compression.State) (gateway.Link, error) {
	userInfo := sess.UserInfo()
	linkID := m.generateLinkID(userInfo)
	// 将 ManagerConfig 转换为 link.Option
	opts := m.convertToLinkOptions(userInfo, compressionState) // 暂时传入nil，后续需要从其他地方获取压缩状态
	lk := New(ctx, linkID, sess, conn, opts...)
	m.links.Store(linkID, lk)
	m.logger.Info("创建新连接",
		elog.String("linkID", linkID),
		elog.Any("userInfo", userInfo),
	)
	m.len.Add(1)
	return lk, nil
}

func (m *Manager) generateLinkID(userInfo session.UserInfo) string {
	return fmt.Sprintf("%d-%d", userInfo.BizID, userInfo.UserID)
}

// convertToLinkOptions 将 ManagerConfig 转换为 link.Option
func (m *Manager) convertToLinkOptions(userInfo session.UserInfo, compressionState *compression.State) []Option {
	var opts []Option

	// 设置压缩状态 - 如果外部提供了压缩状态则使用，否则根据配置创建
	if compressionState != nil {
		opts = append(opts, WithCompression(compressionState))
	}

	// 设置超时配置
	if m.config.ReadTimeout > 0 || m.config.WriteTimeout > 0 {
		opts = append(opts, WithTimeouts(m.config.ReadTimeout, m.config.WriteTimeout))
	}

	// 设置重试配置
	if m.config.MaxRetries > 0 {
		opts = append(opts, WithRetry(
			m.config.InitRetryInterval,
			m.config.MaxRetryInterval,
			m.config.MaxRetries,
		))
	}

	// 设置缓冲区大小
	if m.config.SendBufferSize > 0 || m.config.ReceiveBufferSize > 0 {
		opts = append(opts, WithBuffer(m.config.SendBufferSize, m.config.ReceiveBufferSize))
	}

	opts = append(opts,
		// 设置自动关闭
		WithAutoClose(userInfo.AutoClose),
		// 设置限流器
		WithRateLimit(m.config.UserRateLimit))

	return opts
}

// FindLinkByUserInfo 根据用户会话信息（如用户ID）查找对应的 Link 实例。
func (m *Manager) FindLinkByUserInfo(userInfo session.UserInfo) (gateway.Link, bool) {
	linkID := m.generateLinkID(userInfo)
	return m.links.Load(linkID)
}

// RemoveLink 删除指定的 Link
func (m *Manager) RemoveLink(linkID string) bool {
	_, deleted := m.links.LoadAndDelete(linkID)
	if deleted {
		m.logger.Info("删除连接",
			elog.String("linkID", linkID),
		)
		m.len.Add(-1)
	}
	return deleted
}

// RedirectLinks 重定向选中的连接
func (m *Manager) RedirectLinks(ctx context.Context, selector gateway.LinkSelector, availableNodes *apiv1.NodeList) error {
	if len(availableNodes.GetNodes()) == 0 {
		return fmt.Errorf("没有可用的节点进行重定向")
	}
	body, err := m.codecHelper.Marshal(availableNodes)
	if err != nil {
		return fmt.Errorf("序列化节点信息失败: %w", err)
	}
	m.logger.Info("重定向消息体，可用网关节点信息", elog.String("body", availableNodes.String()))
	msg := &apiv1.Message{
		Cmd:  apiv1.Message_COMMAND_TYPE_REDIRECT,
		Key:  fmt.Sprintf("%d", time.Now().UnixMilli()),
		Body: body,
	}
	return m.PushMessage(ctx, selector, msg)
}

func (m *Manager) PushMessage(_ context.Context, selector gateway.LinkSelector, msg *apiv1.Message) error {
	// 获取所有连接
	allLinks := m.Links()
	if len(allLinks) == 0 {
		m.logger.Info("没有活跃连接")
		return nil
	}

	// 选择需要重定向的连接
	selectedLinks := selector.Select(allLinks)
	if len(selectedLinks) == 0 {
		m.logger.Info("没有连接被选中")
		return nil
	}

	return m.push(selectedLinks, msg)
}

func (m *Manager) push(selectedLinks []gateway.Link, msg *apiv1.Message) error {
	payload, err := m.codecHelper.Marshal(msg)
	if err != nil {
		m.logger.Error("序列化消息失败",
			elog.FieldErr(err),
		)
		return err
	}
	// 向选中的连接发送消息
	for i := range selectedLinks {
		if err1 := selectedLinks[i].Send(payload); err1 != nil {
			m.logger.Error("发送消息失败",
				elog.String("linkID", selectedLinks[i].ID()),
				elog.Any("msg", msg.String()),
				elog.FieldErr(err1),
			)
		} else {
			m.logger.Info("发送消息成功",
				elog.String("linkID", selectedLinks[i].ID()),
				elog.Any("msg", msg.String()),
			)
		}
	}
	return nil
}

// CleanIdleLinks 清理空闲连接
func (m *Manager) CleanIdleLinks(idleTimeout time.Duration) int {
	n := 0
	m.links.Range(func(key string, link *Link) bool {
		if link.TryCloseIfIdle(idleTimeout) {
			if m.RemoveLink(key) {
				n++
				m.logger.Info("清理空闲连接",
					elog.String("linkID", link.ID()),
					elog.Any("userInfo", link.Session().UserInfo()),
				)
			}
		}
		return true
	})
	return n
}

// Len 返回当前管理的 Link 实例总数。
func (m *Manager) Len() int64 {
	return m.len.Load()
}

// Links 返回当前所有 Link 实例的快照切片。
// 注意：这可能是一个耗时操作，应谨慎使用。
func (m *Manager) Links() []gateway.Link {
	var links []gateway.Link
	m.links.Range(func(_ string, link *Link) bool {
		links = append(links, link)
		return true
	})
	return links
}

// Close 强制关闭所有连接
func (m *Manager) Close() error {
	if m.len.Load() == 0 {
		return nil
	}

	m.logger.Info("开始强制关闭连接",
		elog.Int64("linkCount", m.len.Load()),
	)

	m.links.Range(func(key string, link *Link) bool {
		// 强制关闭所有连接
		if err := link.Close(); err != nil {
			m.logger.Error("强制关闭连接失败",
				elog.String("linkID", link.ID()),
				elog.FieldErr(err),
			)
		}
		// 清空连接映射
		m.RemoveLink(key)
		return true
	})

	m.logger.Info("所有连接已强制关闭")
	return nil
}

// GracefulClose 优雅关闭所有连接
func (m *Manager) GracefulClose(ctx context.Context, availableNodes *apiv1.NodeList) error {
	if m.len.Load() == 0 {
		return nil
	}

	m.logger.Info("开始优雅关闭连接",
		elog.Int64("linkCount", m.len.Load()),
	)

	body, err := m.codecHelper.Marshal(availableNodes)
	if err != nil {
		return fmt.Errorf("序列化节点信息失败: %w", err)
	}
	m.logger.Info("重定向消息体，可用网关节点信息", elog.String("body", availableNodes.String()))
	msg := &apiv1.Message{
		Cmd:  apiv1.Message_COMMAND_TYPE_REDIRECT,
		Key:  fmt.Sprintf("%d", time.Now().UnixMilli()),
		Body: body,
	}

	// 向所有连接发送重定向消息
	err = m.push(m.Links(), msg)
	if err != nil {
		m.logger.Error("优雅关闭，向所有连接发送重定向消息失败", elog.FieldErr(err))
	}

	// 等待前端关闭连接
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			remainingCount := m.Len()
			m.logger.Warn("优雅关闭超时",
				elog.Int64("remainingLinks", remainingCount),
			)
			return m.Close()
		case <-ticker.C:
			if m.Len() == 0 {
				m.logger.Info("所有连接已优雅关闭")
				return nil
			}
		}
	}
}

func (m *Manager) GracefulCloseV2(ctx context.Context, msg *apiv1.Message) error {
	if m.len.Load() == 0 {
		return nil
	}

	m.logger.Info("开始优雅关闭连接",
		elog.Int64("linkCount", m.len.Load()),
	)

	err := m.push(m.Links(), msg)
	if err != nil {
		m.logger.Error("优雅关闭，向所有连接发送重定向消息失败", elog.FieldErr(err))
	}

	// 向所有连接发送重定向消息
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			remainingCount := m.Len()
			m.logger.Warn("优雅关闭超时",
				elog.Int64("remainingLinks", remainingCount),
			)
			return m.Close()
		case <-ticker.C:
			if m.Len() == 0 {
				m.logger.Info("所有连接已优雅关闭")
				return nil
			}
		}
	}
}
