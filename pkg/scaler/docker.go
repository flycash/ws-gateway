package scaler

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/registry"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/ego-component/eetcd"
	"github.com/gotomicro/ego/core/elog"
	"go.etcd.io/etcd/client/v3/concurrency"
)

const (
	ScalingLockKey = "/services/gateway/locks/scaling"
)

// ScaleUpResult 封装了扩容操作的结果。
type ScaleUpResult struct {
	SucceededNodes []*apiv1.Node
}

// Scaler 定义了扩缩容的标准接口。
type Scaler interface {
	ScaleUp(ctx context.Context, count int) (*apiv1.NodeList, error)
}

// DockerScaler 是 Scaler 接口基于 Docker 的具体实现。
type DockerScaler struct {
	dockerCli     *client.Client
	etcdClient    *eetcd.Component
	registry      gateway.ServiceRegistry
	imageName     string
	dockerNetwork string
	baseDomain    string

	logger *elog.Component
}

// NewDockerScaler 创建一个新的 DockerScaler 实例。
func NewDockerScaler(
	dockerClient *client.Client,
	etcdClient *eetcd.Component,
	registry *registry.EtcdRegistry,
	imageName, network, domain string) *DockerScaler {
	return &DockerScaler{
		dockerCli:     dockerClient,
		etcdClient:    etcdClient,
		registry:      registry,
		imageName:     imageName,
		dockerNetwork: network,
		baseDomain:    domain,
		logger:        elog.EgoLogger.With(elog.FieldComponent("Scaler.DockerScaler")),
	}
}

// ScaleUp 实现了扩容逻辑，并使用 Etcd 分布式锁来防止并发冲突。
func (ds *DockerScaler) ScaleUp(ctx context.Context, count int) (*apiv1.NodeList, error) {
	session, err := concurrency.NewSession(ds.etcdClient.Client, concurrency.WithTTL(15))
	if err != nil {
		return nil, fmt.Errorf("failed to create etcd session for lock: %w", err)
	}
	defer session.Close()

	mutex := concurrency.NewMutex(session, ScalingLockKey)
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := mutex.TryLock(lockCtx); err != nil {
		log.Printf("[INFO] Could not acquire scaling lock, another operation may be in progress: %v", err)
		return nil, nil // Not an error, just a contention.
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer unlockCancel()
		if err := mutex.Unlock(unlockCtx); err != nil {
			log.Printf("[ERROR] Failed to unlock scaling lock: %v", err)
		} else {
			log.Println("[INFO] Scaling lock released.")
		}
	}()
	log.Println("[INFO] Scaling lock acquired. Starting scale-up process.")

	nodes, err := ds.registry.GetAvailableNodes(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get existing nodes before scaling: %w", err)
	}
	maxID := 0
	for _, node := range nodes {
		if id, err := strconv.Atoi(strings.TrimPrefix(node.GetId(), "gw-")); err == nil {
			if id > maxID {
				maxID = id
			}
		}
	}
	startID := maxID + 1

	result := &apiv1.NodeList{Nodes: make([]*apiv1.Node, 0, count)}
	for i := 0; i < count; i++ {
		newNodeID := fmt.Sprintf("gw-%d", startID+i)
		publicAddr := fmt.Sprintf("wss://%s.%s", newNodeID, ds.baseDomain)
		containerName := fmt.Sprintf("gateway-%s", newNodeID)

		containerConfig := &container.Config{
			Image: ds.imageName,
			Env: []string{
				"NODE_ID=" + newNodeID,
				"PUBLIC_ADDR=" + publicAddr,
				"ETCD_ENDPOINTS=etcd:2379",
				"NODE_CAPACITY=10000", // 新节点的容量配置
			},
			Labels: map[string]string{"prometheus-job": "ws-gateway"},
		}
		networkConfig := &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{ds.dockerNetwork: {}},
		}

		resp, err := ds.dockerCli.ContainerCreate(ctx, containerConfig, nil, networkConfig, nil, containerName)
		if err != nil {
			log.Printf("[ERROR] Failed to create container %s: %v", containerName, err)
			continue
		}
		if err := ds.dockerCli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			log.Printf("[ERROR] Failed to start container %s: %v", containerName, err)
			continue
		}

		log.Printf("[INFO] Successfully started new node: %s (Container ID: %s)", newNodeID, resp.ID)
		// 注意：这里需要从配置中获取新节点的容量
		capacity, _ := strconv.ParseInt(strings.Split(containerConfig.Env[3], "=")[1], 10, 64)
		newNodeInfo := &apiv1.Node{Id: newNodeID, Ip: publicAddr, Capacity: capacity}
		result.Nodes = append(result.Nodes, newNodeInfo)
	}
	return result, nil
}
