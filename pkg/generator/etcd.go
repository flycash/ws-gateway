package generator

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/gotomicro/ego/core/elog"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	// idGenPrefix 是所有生成器相关 key 的前缀。
	idGenPrefix = "/service/gateway/gen/"
	// nodeIDKey 是用于生成节点ID的 key。
	nodeIDKey = idGenPrefix + "id"
	// portKey 是用于生成端口号的 key。
	portKey = idGenPrefix + "port"

	// retryInterval 定义了在获取ID/Port失败时的重试间隔。
	retryInterval = 50 * time.Millisecond
)

// IDGenerator 定义了分布式唯一ID和端口生成器的接口。
type IDGenerator interface {
	// GenerateNodeID 会在 Etcd 中原子性地获取并递增一个全局的节点ID。
	GenerateNodeID(ctx context.Context) (int64, error)

	// GeneratePort 会在 Etcd 中原子性地获取并递增一个全局的端口号。
	GeneratePort(ctx context.Context) (int64, error)
}

// EtcdIDGenerator 是基于 Etcd 实现的 IDGenerator。
type EtcdIDGenerator struct {
	client *clientv3.Client
	logger *elog.Component
}

// NewEtcdIDGenerator 创建一个新的 EtcdIDGenerator 实例。
// 它会尝试在 Etcd 中初始化 ID 和 Port 的起始值（如果它们还不存在）。
func NewEtcdIDGenerator(ctx context.Context, client *clientv3.Client, initialNodeID, initialPort int64) (*EtcdIDGenerator, error) {
	g := &EtcdIDGenerator{
		client: client,
		logger: elog.EgoLogger.With(elog.FieldComponent("IDGenerator")),
	}

	// 尝试原子性地初始化 node ID
	if err := g.initializeKey(ctx, nodeIDKey, initialNodeID); err != nil {
		return nil, fmt.Errorf("初始化节点ID失败: %w", err)
	}

	// 尝试原子性地初始化 port
	if err := g.initializeKey(ctx, portKey, initialPort); err != nil {
		return nil, fmt.Errorf("初始化端口失败: %w", err)
	}

	g.logger.Info("ID/Port 生成器初始化成功")
	return g, nil
}

// initializeKey 尝试创建一个 key，仅当该 key 不存在时。
// 这是一个原子的 "Create-If-NotExist" 操作。
func (g *EtcdIDGenerator) initializeKey(ctx context.Context, key string, value int64) error {
	// 创建一个事务，条件是 key 的 CreateRevision 为 0 (即 key 不存在)
	txn := g.client.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, strconv.FormatInt(value, 10)))

	resp, err := txn.Commit()
	if err != nil {
		return fmt.Errorf("事务提交失败: %w", err)
	}

	if resp.Succeeded {
		g.logger.Info("成功初始化 key", elog.String("key", key), elog.Int64("value", value))
	} else {
		g.logger.Info("Key 已存在，跳过初始化", elog.String("key", key))
	}

	return nil
}

// GenerateNodeID 实现获取唯一节点 ID 的逻辑。
func (g *EtcdIDGenerator) GenerateNodeID(ctx context.Context) (int64, error) {
	return g.generateValue(ctx, nodeIDKey)
}

// GeneratePort 实现获取唯一端口号的逻辑。
func (g *EtcdIDGenerator) GeneratePort(ctx context.Context) (int64, error) {
	return g.generateValue(ctx, portKey)
}

// generateValue是通用的生成逻辑，通过 CAS 操作原子性地递增 key 的值。
func (g *EtcdIDGenerator) generateValue(ctx context.Context, key string) (int64, error) {
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}

		// 1. 获取当前的值和版本号
		resp, err := g.client.Get(ctx, key)
		if err != nil {
			g.logger.Error("获取 key 失败，准备重试", elog.String("key", key), elog.FieldErr(err))
			time.Sleep(retryInterval)
			continue
		}

		if len(resp.Kvs) == 0 {
			// 这种情况理论上不应该发生，因为 NewEtcdIDGenerator 会初始化 key。
			// 但作为防御性编程，我们处理它。
			return 0, fmt.Errorf("key '%s' 在 Etcd 中不存在，生成器未正确初始化", key)
		}

		kv := resp.Kvs[0]
		currentValue, err := strconv.ParseInt(string(kv.Value), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("解析存储的值失败: %w", err)
		}

		// 我们获取到的ID是已经被占用的，所以我们需要的是下一个ID
		nextValue := currentValue + 1

		// 2. 尝试使用 CAS 更新值
		// 条件是：key 的 ModRevision 必须还是我们刚刚 GET 到的那个版本。
		txn := g.client.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(key), "=", kv.ModRevision)).
			Then(clientv3.OpPut(key, strconv.FormatInt(nextValue, 10)))

		txnResp, err := txn.Commit()
		if err != nil {
			g.logger.Error("CAS 事务提交失败，准备重试", elog.String("key", key), elog.FieldErr(err))
			time.Sleep(retryInterval)
			continue
		}

		// 3. 检查事务是否成功
		if txnResp.Succeeded {
			// 成功！我们抢到了这个ID
			g.logger.Info("成功生成值", elog.String("key", key), elog.Int64("value", nextValue))
			return nextValue, nil
		}

		// 事务失败，意味着有其他节点在我们之前更新了 key。
		// 无需做任何事，循环将自动进行下一次尝试。
		g.logger.Warn("CAS 操作失败，发生竞争，立即重试", elog.String("key", key))
	}
}
