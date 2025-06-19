package scaler

import (
	"context"
	"fmt"
	"strconv"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/ego-component/eetcd"
	"github.com/gotomicro/ego/core/elog"
	"go.etcd.io/etcd/client/v3/concurrency"
)

const (
	ScalingLockKey = "/services/gateway/locks/scaling"

	// 扩容锁相关常量

	SessionTTLSeconds    = 15
	LockTimeoutSeconds   = 5
	UnlockTimeoutSeconds = 5

	// 节点等待相关常量

	MaxWaitTimeSeconds   = 60
	CheckIntervalSeconds = 2

	// 默认配置

	DefaultNodeCapacity = 10
	DefaultStopTimeout  = 30

	// 动态分配起始值

	DefaultStartNodeID = 1    // 第一个动态分配的节点ID将是2
	DefaultStartPort   = 9002 // 第一个动态分配的端口将是9003
)

// ScaleUpResult 封装了扩容操作的结果。
type ScaleUpResult struct {
	SucceededNodes []*apiv1.Node
}

// Scaler 定义了扩缩容的标准接口。
// ScaleUp 执行完整的扩容操作，包括创建容器、等待注册成功，返回已注册的节点信息
type Scaler interface {
	ScaleUp(ctx context.Context, count int) (*apiv1.NodeList, error)
}

type HostIPFunc func(ctx context.Context) (string, error)

// DockerScaler 是 Scaler 接口基于 Docker 的具体实现。
type DockerScaler struct {
	dockerCli            *client.Client
	etcdClient           *eetcd.Component
	registry             gateway.ServiceRegistry
	currentNodeID        string // 当前节点ID，用于查询可用节点
	internalPort         string
	hostIPFunc           HostIPFunc
	dockerComposeProject string // docker-compose 项目名称，用于生成网络名和标签
	imageNamePattern     string // 镜像名称模式，如 "ws-gateway-*"

	logger *elog.Component
}

// NewDockerScaler 创建一个新的 DockerScaler 实例。
func NewDockerScaler(
	dockerClient *client.Client,
	etcdClient *eetcd.Component,
	registry gateway.ServiceRegistry,
	currentNodeID string,
	internalPort string, // 9002
	hostIPFunc HostIPFunc,
	imageNamePattern, dockerComposeProject string,
) *DockerScaler {
	return &DockerScaler{
		dockerCli:            dockerClient,
		etcdClient:           etcdClient,
		registry:             registry,
		currentNodeID:        currentNodeID,
		internalPort:         internalPort,
		hostIPFunc:           hostIPFunc,
		imageNamePattern:     imageNamePattern,
		dockerComposeProject: dockerComposeProject,
		logger:               elog.EgoLogger.With(elog.FieldComponent("Scaler.DockerScaler")),
	}
}

// ScaleUp 实现了完整的扩容逻辑，包括创建容器和等待注册成功
func (d *DockerScaler) ScaleUp(ctx context.Context, count int) (*apiv1.NodeList, error) {
	// 获取分布式锁
	session, mutex, err := d.acquireScalingLock(ctx)
	if err != nil {
		return nil, err
	}
	if session == nil {
		// 锁竞争失败，但不是错误
		return &apiv1.NodeList{Nodes: []*apiv1.Node{}}, nil
	}
	defer session.Close()
	defer d.releaseScalingLock(ctx, mutex)

	// 查找最新镜像
	imageName, err := d.findLatestImage(ctx)
	if err != nil {
		return nil, fmt.Errorf("查找最新镜像失败: %w", err)
	}
	d.logger.Info("找到最新镜像", elog.String("imageName", imageName))

	// 创建新节点
	createdNodes, err := d.createNewNodes(ctx, count, imageName)
	if err != nil {
		d.logger.Error("创建新节点失败", elog.FieldErr(err))
		return nil, fmt.Errorf("创建新节点失败: %w", err)
	}
	if len(createdNodes.Nodes) == 0 {
		d.logger.Warn("没有成功创建任何新节点")
		return &apiv1.NodeList{Nodes: []*apiv1.Node{}}, nil
	}

	// 等待新节点注册成功
	registeredNodes, err := d.waitForNodesRegistration(ctx, createdNodes.Nodes)
	if err != nil {
		return nil, fmt.Errorf("等待节点注册失败: %w", err)
	}

	d.logger.Info("扩容操作完成",
		elog.Int("createdCount", len(createdNodes.Nodes)),
		elog.Int("registeredCount", len(registeredNodes.Nodes)))

	return registeredNodes, nil
}

// acquireScalingLock 获取扩容分布式锁
func (d *DockerScaler) acquireScalingLock(ctx context.Context) (*concurrency.Session, *concurrency.Mutex, error) {
	session, err := concurrency.NewSession(d.etcdClient.Client, concurrency.WithTTL(SessionTTLSeconds))
	if err != nil {
		return nil, nil, fmt.Errorf("创建用于加锁的etcd会话失败: %w", err)
	}

	mutex := concurrency.NewMutex(session, ScalingLockKey)
	lockCtx, cancel := context.WithTimeout(ctx, LockTimeoutSeconds*time.Second)
	defer cancel()

	if err := mutex.TryLock(lockCtx); err != nil {
		session.Close()
		d.logger.Warn("无法获取扩容锁，可能有其他扩容操作正在进行", elog.FieldErr(err))
		return nil, nil, nil // 不是错误，只是锁竞争
	}

	d.logger.Info("获取扩容锁成功，开始扩容流程")
	return session, mutex, nil
}

// releaseScalingLock 释放扩容分布式锁
func (d *DockerScaler) releaseScalingLock(ctx context.Context, mutex *concurrency.Mutex) {
	unlockCtx, unlockCancel := context.WithTimeout(ctx, UnlockTimeoutSeconds*time.Second)
	defer unlockCancel()

	if err := mutex.Unlock(unlockCtx); err != nil {
		d.logger.Error("释放扩容锁失败", elog.FieldErr(err))
	} else {
		d.logger.Info("扩容锁已释放")
	}
}

// findLatestImage 查找最新的网关镜像
func (d *DockerScaler) findLatestImage(ctx context.Context) (string, error) {
	// 构建镜像过滤器
	imageFilters := filters.NewArgs()
	imageFilters.Add("reference", d.imageNamePattern)

	// 列出镜像
	images, err := d.dockerCli.ImageList(ctx, image.ListOptions{
		Filters: imageFilters,
	})
	if err != nil {
		return "", fmt.Errorf("列出镜像失败: %w", err)
	}

	if len(images) == 0 {
		return "", fmt.Errorf("没有找到匹配模式 %s 的镜像", d.imageNamePattern)
	}

	// 按创建时间排序，选择最新的镜像
	latestImageIndex := -1
	var latestTime int64 = 0

	for i := range images {
		if images[i].Created > latestTime {
			latestTime = images[i].Created
			latestImageIndex = i
		}
	}

	if latestImageIndex == -1 {
		return "", fmt.Errorf("没有找到有效的镜像")
	}

	// 获取镜像的第一个标签
	latestImage := images[latestImageIndex]
	if len(latestImage.RepoTags) == 0 {
		return "", fmt.Errorf("找到的镜像没有标签")
	}

	return latestImage.RepoTags[0], nil
}

// createNewNodes 创建新的网关节点
func (d *DockerScaler) createNewNodes(ctx context.Context, count int, imageName string) (*apiv1.NodeList, error) {
	result := &apiv1.NodeList{Nodes: make([]*apiv1.Node, 0, count)}
	hostIP, err := d.hostIPFunc(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取节点宿主IP失败: %w", err)
	}
	for i := 0; i < count; i++ {
		nodeInfo, err := d.createSingleNode(ctx, imageName, hostIP)
		if err != nil {
			d.logger.Error("创建节点失败", elog.FieldErr(err))
			continue
		}
		result.Nodes = append(result.Nodes, nodeInfo)
	}

	d.logger.Info("扩容操作完成",
		elog.Int("requestedCount", count),
		elog.Int("successCount", len(result.Nodes)))

	return result, nil
}

// createSingleNode 创建单个网关节点
func (d *DockerScaler) createSingleNode(ctx context.Context, imageName, hostIP string) (*apiv1.Node, error) {
	// 获取下一个可用的节点ID和端口（基于现有节点的最大值）
	nodeID, hostPort, err := d.getNextAvailableNodeIDAndPort(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取可用节点ID和端口失败: %w", err)
	}

	d.logger.Info("新节点ID和端口",
		elog.Int64("nodeID", nodeID),
		elog.Int64("hostPort", hostPort))

	newNodeID := fmt.Sprintf("gw-%d", nodeID)
	containerName := fmt.Sprintf("gateway-%d-1", nodeID)

	// 创建和启动容器
	containerID, err := d.createAndStartContainer(ctx, containerName, newNodeID, imageName, hostIP, hostPort)
	if err != nil {
		return nil, err
	}

	d.logger.Info("成功启动新节点",
		elog.String("nodeID", newNodeID),
		elog.String("containerID", containerID),
		elog.String("hostIP", hostIP),
		elog.Int64("hostPort", hostPort))

	return &apiv1.Node{
		Id:       newNodeID,
		Ip:       hostIP,
		Port:     int32(hostPort),
		Capacity: DefaultNodeCapacity,
	}, nil
}

// getNextAvailableNodeIDAndPort 获取下一个可用的节点ID和端口，基于现有节点的最大值
func (d *DockerScaler) getNextAvailableNodeIDAndPort(ctx context.Context) (nodeID, port int64, err error) {
	// 获取所有可用节点（包括当前节点）
	availableNodes, err := d.registry.GetAvailableNodes(ctx, "")
	if err != nil {
		return 0, 0, fmt.Errorf("获取可用节点失败: %w", err)
	}

	d.logger.Info("获取全部节点信息（包含当前节点）",
		elog.String("availableNodes", availableNodes.String()))

	// 找到最大节点ID和端口号
	// 如果没有现有节点，从1开始分配节点ID，从9003开始分配端口（避开9002这个内部端口）
	maxNodeID := int64(DefaultStartNodeID) // 如果没有节点，下一个将是1
	maxPort := int64(DefaultStartPort)     // 如果没有节点，下一个将是9003

	nodes := availableNodes.GetNodes()
	for _, node := range nodes {
		// 解析节点ID中的数字部分 (gw-1 -> 1)
		if nodeIDNum := d.extractNodeIDNumber(node.Id); nodeIDNum > maxNodeID {
			maxNodeID = nodeIDNum
		}

		if int64(node.Port) > maxPort {
			maxPort = int64(node.Port)
		}
	}

	return maxNodeID + 1, maxPort + 1, nil
}

// extractNodeIDNumber 从节点ID中提取数字部分 (gw-3 -> 3)
func (d *DockerScaler) extractNodeIDNumber(nodeID string) int64 {
	// 简单实现：假设格式为 gw-{number}
	if len(nodeID) > 3 && nodeID[:3] == "gw-" {
		if num, err := strconv.ParseInt(nodeID[3:], 10, 64); err == nil {
			return num
		}
	}
	return 0
}

// createAndStartContainer 创建并启动容器
func (d *DockerScaler) createAndStartContainer(ctx context.Context, containerName, nodeID, imageName, hostIP string, hostPort int64) (string, error) {
	// 配置容器
	port := nat.Port(d.internalPort + "/tcp")
	containerConfig := &container.Config{
		Image: imageName,
		Env: []string{
			"GATEWAY_NODE_ID=" + nodeID,
			"GATEWAY_NODE_HOST_IP=" + hostIP,
			"GATEWAY_NODE_HOST_PORT=" + strconv.FormatInt(hostPort, 10),
			"GATEWAY_STOP_TIMEOUT=" + strconv.Itoa(DefaultStopTimeout),
			"EGO_DEBUG=true",
		},
		Labels: map[string]string{
			"job":                        "ws-gateway",
			"node_id":                    nodeID,
			"metrics_port":               "9003",
			"com.docker.compose.project": d.dockerComposeProject,
			"dynamic-scaling":            "true",
		},
		// 有些网络驱动要求 ExposedPorts 才能完成 nat 绑定
		ExposedPorts: nat.PortSet{port: struct{}{}},
	}

	// 配置端口映射
	portBindings := nat.PortMap{}
	portBindings[port] = []nat.PortBinding{
		{
			HostIP:   "0.0.0.0",
			HostPort: strconv.FormatInt(hostPort, 10),
		},
	}

	// 配置网络 - Docker Compose自动创建的网络名格式为: {项目名}_default
	networkName := d.dockerComposeProject + "_default"

	d.logger.Info("配置容器网络",
		elog.String("networkName", networkName),
		elog.String("dockerComposeProject", d.dockerComposeProject))

	hostConfig := &container.HostConfig{
		NetworkMode:  container.NetworkMode(networkName), // 设置正确的网络模式
		PortBindings: portBindings,
		// 允许容器访问宿主机服务（如IM后端服务）
		ExtraHosts: []string{
			"host.docker.internal:host-gateway",
		},
		// 挂载Docker socket以支持容器扩容功能
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: "/var/run/docker.sock",
				Target: "/var/run/docker.sock",
			},
		},
	}

	// 创建容器（不需要单独的networkConfig，NetworkMode已经处理了网络配置）
	resp, err := d.dockerCli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
	if err != nil {
		return "", fmt.Errorf("创建容器失败: %w", err)
	}

	// 启动容器
	if err := d.dockerCli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("启动容器失败: %w", err)
	}

	return resp.ID, nil
}

// waitForNodesRegistration 等待新节点注册到服务中心
func (d *DockerScaler) waitForNodesRegistration(ctx context.Context, expectedNodes []*apiv1.Node) (*apiv1.NodeList, error) {
	maxWaitTime := time.Duration(MaxWaitTimeSeconds) * time.Second
	checkInterval := time.Duration(CheckIntervalSeconds) * time.Second
	timeout := time.After(maxWaitTime)
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	d.logger.Info("开始等待新节点注册",
		elog.Int("expectedCount", len(expectedNodes)),
		elog.Duration("maxWaitTime", maxWaitTime))

	for {
		select {
		case <-timeout:
			d.logger.Error("等待新节点注册超时",
				elog.Duration("timeout", maxWaitTime),
				elog.Int("expectedCount", len(expectedNodes)))
			return nil, fmt.Errorf("等待新节点注册超时")

		case <-ticker.C:
			// 获取所有可用节点
			availableNodes, err := d.registry.GetAvailableNodes(ctx, d.currentNodeID)
			if err != nil {
				d.logger.Error("检查可用节点失败", elog.FieldErr(err))
				continue
			}

			// 检查新节点是否已注册
			registeredNodes := d.findRegisteredNodes(expectedNodes, availableNodes.GetNodes())

			d.logger.Debug("检查新节点注册状态",
				elog.Int("expectedCount", len(expectedNodes)),
				elog.Int("registeredCount", len(registeredNodes)))

			// 如果所有新节点都已注册，返回结果
			if len(registeredNodes) == len(expectedNodes) {
				d.logger.Info("所有新节点已成功注册到服务中心")
				return &apiv1.NodeList{Nodes: registeredNodes}, nil
			}
		}
	}
}

// findRegisteredNodes 在可用节点列表中查找已注册的新节点
func (d *DockerScaler) findRegisteredNodes(expectedNodes, availableNodes []*apiv1.Node) []*apiv1.Node {
	var registeredNodes []*apiv1.Node

	for _, expected := range expectedNodes {
		for _, available := range availableNodes {
			if expected.Id == available.Id {
				registeredNodes = append(registeredNodes, available)
				break
			}
		}
	}

	return registeredNodes
}
