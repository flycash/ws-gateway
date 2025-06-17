package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/ecodeclub/ekit/retry"
	"github.com/ego-component/eetcd"
	"github.com/gotomicro/ego/core/elog"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	// GatewayRegistryPrefix 是 Etcd 中存储所有网关节点信息键的前缀。
	// 所有节点信息都将存储在类似 `/gateways/node/${ID}` 的键下。
	GatewayRegistryPrefix = "/gateways/node/"

	// LeaseTTL 定义了节点注册到 Etcd 时使用的租约秒数 (Time-To-Live)。
	// 节点必须在此时间内续租，否则其注册信息将因租约到期而被 Etcd 自动删除。
	LeaseTTL = 10

	// GracefulShutdownWaitTime 优雅关闭时的等待时间，让其他节点感知到权重变化
	GracefulShutdownWaitTime = 5 * time.Second
)

var (
	_                     gateway.ServiceRegistry = (*EtcdRegistry)(nil)
	ErrNodeInfoNotExist                           = errors.New("节点信息不存在")
	ErrMaxRetriesExceeded                         = errors.New("最大重试次数已耗尽")
)

// EtcdRegistry 是 ServiceRegistry 接口基于 Etcd 的具体实现。
type EtcdRegistry struct {
	client                    *eetcd.Component
	registerInitRetryInterval time.Duration
	registerMaxRetryInterval  time.Duration
	registerMaxRetries        int32

	keepAliveRetryInterval time.Duration
	keepAliveMaxRetries    int

	ctx    context.Context
	cancel context.CancelFunc

	logger *elog.Component
}

// NewEtcdRegistry 创建一个新的 EtcdRegistry 实例。
func NewEtcdRegistry(client *eetcd.Component,
	registerInitRetryInterval,
	registerMaxRetryInterval time.Duration,
	registerMaxRetries int32,
	keepAliveRetryInterval time.Duration,
	keepAliveMaxRetries int,
) *EtcdRegistry {
	ctx, cancel := context.WithCancel(context.Background())
	return &EtcdRegistry{
		client:                    client,
		registerInitRetryInterval: registerInitRetryInterval,
		registerMaxRetryInterval:  registerMaxRetryInterval,
		registerMaxRetries:        registerMaxRetries,
		keepAliveRetryInterval:    keepAliveRetryInterval,
		keepAliveMaxRetries:       keepAliveMaxRetries,
		ctx:                       ctx,
		cancel:                    cancel,
		logger:                    elog.EgoLogger.With(elog.FieldComponent("ServiceRegistry")),
	}
}

// Register 实现了将节点信息（JSON 格式）注册到 Etcd 的逻辑。
func (r *EtcdRegistry) Register(ctx context.Context, node *apiv1.Node) (leaseID clientv3.LeaseID, err error) {
	// 1. 将 Node 对象通过 JSON 序列化
	nodeBytes, err := json.Marshal(node)
	if err != nil {
		r.logger.Error("序列化节点信息失败",
			elog.String("nodeID", node.GetId()),
			elog.FieldErr(err))
		return 0, fmt.Errorf("序列化节点信息失败: %w", err)
	}

	// 2. 创建重试策略
	strategy, err := retry.NewExponentialBackoffRetryStrategy(r.registerInitRetryInterval, r.registerMaxRetryInterval, r.registerMaxRetries)
	if err != nil {
		r.logger.Warn("初始化重试策略失败", elog.FieldErr(err))
		return 0, fmt.Errorf("初始化重试策略失败: %w", err)
	}

	for {
		// 3. 创建一个租约
		leaseResp, err := r.client.Grant(ctx, LeaseTTL)
		if err != nil {
			r.logger.Error("创建租约失败", elog.FieldErr(err))
			return 0, fmt.Errorf("创建租约失败: %w", err)
		}

		// 4. 将序列化后的数据与租约关联，存入 Etcd
		_, err = r.client.Put(ctx, r.key(node.GetId()), string(nodeBytes), clientv3.WithLease(leaseResp.ID))
		if err == nil {
			r.logger.Info("节点注册成功",
				elog.String("nodeID", node.GetId()),
				elog.String("nodeIP", node.GetIp()),
				elog.Int32("nodePort", node.GetPort()),
				elog.Int64("leaseID", int64(leaseResp.ID)))
			return leaseResp.ID, nil

		}

		// 5. 如果 Put 失败，应立即撤销已创建的租约，避免资源泄露
		_, _ = r.client.Revoke(ctx, leaseResp.ID)

		// 6. 等待重试
		duration, ok := strategy.Next()
		if !ok {
			r.logger.Warn("注册节点信息到Etcd失败，准备重试",
				elog.String("nodeID", node.GetId()),
				elog.FieldErr(err))
			return 0, fmt.Errorf("注册节点信息到Etcd失败: %w: %w", err, ErrMaxRetriesExceeded)
		}
		time.Sleep(duration)
	}
}

func (r *EtcdRegistry) key(nodeID string) string {
	return GatewayRegistryPrefix + nodeID
}

// KeepAlive 实现了为租约续期的逻辑。
func (r *EtcdRegistry) KeepAlive(ctx context.Context, leaseID clientv3.LeaseID) error {
	keepAliveChan, err := r.client.KeepAlive(ctx, leaseID)
	if err != nil {
		r.logger.Error("启动租约续期失败",
			elog.Int64("leaseID", int64(leaseID)),
			elog.FieldErr(err))
		return err
	}

	retryCount := 0

	for {
		select {
		case resp, ok := <-keepAliveChan:
			if !ok {
				r.logger.Warn("租约续期通道关闭，尝试重新启动续期",
					elog.Int64("leaseID", int64(leaseID)),
					elog.Int("retryCount", retryCount))

				if retryCount >= r.keepAliveMaxRetries {
					r.logger.Error("租约续期失败，已达到最大重试次数",
						elog.Int64("leaseID", int64(leaseID)))
					return ErrMaxRetriesExceeded
				}

				retryCount++
				time.Sleep(r.keepAliveRetryInterval) // 使用配置

				// 重新启动续期
				keepAliveChan, err = r.client.KeepAlive(ctx, leaseID)
				if err != nil {
					r.logger.Error("重新启动租约续期失败",
						elog.Int64("leaseID", int64(leaseID)),
						elog.Int("retryCount", retryCount),
						elog.FieldErr(err))
					if retryCount >= r.keepAliveMaxRetries {
						r.logger.Error("租约续期重试次数达到上限，停止续期",
							elog.Int64("leaseID", int64(leaseID)))
						return ErrMaxRetriesExceeded
					}
				}
				r.logger.Info("重新启动租约续期成功",
					elog.Int64("leaseID", int64(leaseID)),
					elog.Int("retryCount", retryCount))
				retryCount = 0 // 重置重试计数
				continue
			}

			if resp == nil {
				r.logger.Warn("收到空的续期响应", elog.Int64("leaseID", int64(leaseID)))
				continue
			}

			// 续期成功，重置重试计数
			retryCount = 0
			// r.logger.Info("租约续期成功", elog.Int64("leaseID", int64(leaseID)))

		case <-ctx.Done():
			r.logger.Info("参数上下文取消，停止租约续期", elog.Int64("leaseID", int64(leaseID)))
			return nil
		case <-r.ctx.Done():
			r.logger.Info("内部上下文取消，停止租约续期", elog.Int64("leaseID", int64(leaseID)))
			return nil
		}
	}
}

// Deregister 实现了从 Etcd 注销节点的逻辑。
func (r *EtcdRegistry) Deregister(ctx context.Context, leaseID clientv3.LeaseID, nodeID string) error {
	// 撤销租约，Etcd 会自动删除所有与该租约关联的键值对
	if _, err := r.client.Revoke(ctx, leaseID); err != nil {
		r.logger.Error("撤销租约失败",
			elog.String("nodeID", nodeID),
			elog.Int64("leaseID", int64(leaseID)),
			elog.FieldErr(err))
		return fmt.Errorf("撤销租约失败，节点ID: %s, 租约ID: %d, 错误: %w", nodeID, leaseID, err)
	}
	r.logger.Info("节点注销成功",
		elog.String("nodeID", nodeID),
		elog.Int64("leaseID", int64(leaseID)))
	r.cancel()
	return nil
}

// GracefulDeregister 实现了优雅注销节点的逻辑。
func (r *EtcdRegistry) GracefulDeregister(ctx context.Context, leaseID clientv3.LeaseID, nodeID string) error {
	r.logger.Info("开始优雅注销节点",
		elog.String("nodeID", nodeID),
		elog.Int64("leaseID", int64(leaseID)))

	// 1. 获取当前节点信息
	node, err := r.getNodeInfo(ctx, nodeID)
	if err != nil && !errors.Is(err, ErrNodeInfoNotExist) {
		r.logger.Error("获取节点信息失败",
			elog.String("nodeID", nodeID),
			elog.FieldErr(err))
		return fmt.Errorf("获取节点信息失败: %w", err)
	}

	if errors.Is(err, ErrNodeInfoNotExist) {
		r.logger.Warn("节点不存在，直接返回",
			elog.String("nodeID", nodeID))
		return nil
	}

	// 2. 将节点权重设为0，表示准备关闭
	originalWeight := node.Weight
	node.Weight = 0

	if err := r.UpdateNodeInfo(ctx, leaseID, node); err != nil {
		r.logger.Error("更新节点权重失败",
			elog.String("nodeID", nodeID),
			elog.FieldErr(err))
		return fmt.Errorf("更新节点权重失败: %w", err)
	}

	r.logger.Info("节点权重已设为0，等待其他节点感知变化",
		elog.String("nodeID", nodeID),
		elog.Int32("originalWeight", originalWeight),
		elog.Duration("waitTime", GracefulShutdownWaitTime))

	// 3. 等待一段时间，让其他节点感知到权重变化
	select {
	case <-time.After(GracefulShutdownWaitTime):
		// 正常等待结束
	case <-ctx.Done():
		r.logger.Warn("参数上下文取消，提前结束等待", elog.String("nodeID", nodeID))
		// 继续执行注销逻辑
	}

	// 4. 最终注销节点
	if err2 := r.Deregister(ctx, leaseID, nodeID); err2 != nil {
		r.logger.Error("最终注销节点失败",
			elog.String("nodeID", nodeID),
			elog.FieldErr(err2))
		return fmt.Errorf("最终注销节点失败: %w", err2)
	}

	r.logger.Info("节点优雅注销完成", elog.String("nodeID", nodeID))
	return nil
}

// getNodeInfo 获取指定节点信息（辅助方法）
func (r *EtcdRegistry) getNodeInfo(ctx context.Context, nodeID string) (*apiv1.Node, error) {
	resp, err := r.client.Get(ctx, r.key(nodeID))
	if err != nil {
		return nil, fmt.Errorf("获取节点信息失败: %w", err)
	}

	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("%w: 节点不存在: %s", ErrNodeInfoNotExist, nodeID)
	}

	var node apiv1.Node
	err = json.Unmarshal(resp.Kvs[0].Value, &node)
	if err != nil {
		return nil, fmt.Errorf("反序列化节点信息失败: %w", err)
	}
	return &node, nil
}

// GetAvailableNodes 实现了从 Etcd 获取并反序列化所有可用节点信息的逻辑。
func (r *EtcdRegistry) GetAvailableNodes(ctx context.Context, selfID string) ([]*apiv1.Node, error) {
	// 1. 从 Etcd 获取指定前缀下的所有键值对
	resp, err := r.client.Get(ctx, GatewayRegistryPrefix, clientv3.WithPrefix())
	if err != nil {
		r.logger.Error("从Etcd获取节点信息失败", elog.FieldErr(err))
		return nil, fmt.Errorf("从Etcd获取节点信息失败: %w", err)
	}

	// 2. 遍历结果并进行 JSON 反序列化
	nodes := make([]*apiv1.Node, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		node := &apiv1.Node{}
		// 将二进制数据反序列化到 Node 对象中
		if err := json.Unmarshal(kv.Value, node); err == nil {
			// 排除自身节点
			if node.GetId() != selfID {
				nodes = append(nodes, node)
			}
		} else {
			r.logger.Warn("反序列化节点信息失败",
				elog.String("key", string(kv.Key)),
				elog.FieldErr(err))
		}
	}

	r.logger.Info("获取可用节点成功",
		elog.String("selfID", selfID),
		elog.Int("nodeCount", len(nodes)))

	return nodes, nil
}

// UpdateNodeInfo 实现了更新 Etcd 中节点信息的逻辑。
func (r *EtcdRegistry) UpdateNodeInfo(ctx context.Context, leaseID clientv3.LeaseID, node *apiv1.Node) error {
	// 1. 将新的 Node 对象序列化
	nodeBytes, err := json.Marshal(node)
	if err != nil {
		r.logger.Error("序列化更新的节点信息失败",
			elog.String("nodeID", node.GetId()),
			elog.FieldErr(err))
		return fmt.Errorf("序列化更新的节点信息失败: %w", err)
	}

	// 2. 使用相同的 Key 和租约 ID，覆盖写入 Etcd 中的数据
	_, err = r.client.Put(ctx, r.key(node.GetId()), string(nodeBytes), clientv3.WithLease(leaseID))
	if err != nil {
		r.logger.Error("更新节点信息到Etcd失败",
			elog.String("nodeID", node.GetId()),
			elog.Int64("leaseID", int64(leaseID)),
			elog.FieldErr(err))
		return fmt.Errorf("更新节点信息到Etcd失败，节点ID: %s, 错误: %w", node.GetId(), err)
	}

	r.logger.Info("节点信息更新成功",
		elog.String("nodeID", node.GetId()),
		elog.Int64("load", node.GetLoad()))

	return nil
}

// StartNodeStateUpdater 启动节点状态更新器，定期更新节点状态信息
func (r *EtcdRegistry) StartNodeStateUpdater(ctx context.Context, leaseID clientv3.LeaseID, nodeID string, updateFunc func(node *apiv1.Node) bool, interval time.Duration) error {
	r.logger.Info("启动节点状态更新器",
		elog.String("nodeID", nodeID),
		elog.Duration("interval", interval))

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// 获取当前节点信息
			node, err := r.getNodeInfo(ctx, nodeID)
			if err != nil {
				r.logger.Warn("获取当前节点信息失败，跳过本次状态更新",
					elog.String("nodeID", nodeID),
					elog.FieldErr(err))
				continue
			}

			if updateFunc(node) {
				if err1 := r.UpdateNodeInfo(ctx, leaseID, node); err1 != nil {
					r.logger.Warn("更新节点状态失败",
						elog.String("nodeID", nodeID),
						elog.FieldErr(err1))
				} else {
					r.logger.Info("节点状态更新成功",
						elog.String("nodeID", nodeID),
					)
				}
			}

		case <-ctx.Done():
			r.logger.Info("参数上下文取消，节点状态更新器停止",
				elog.String("nodeID", nodeID))
			return nil
		case <-r.ctx.Done():
			r.logger.Info("内部上下文取消，节点状态更新器停止",
				elog.String("nodeID", nodeID))
			return nil
		}
	}
}
