package registry

//
// import (
// 	"context"
// 	"os"
// 	"os/signal"
// 	"syscall"
// 	"time"
//
// 	gateway "gitee.com/flycash/ws-gateway"
// 	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
// 	"gitee.com/flycash/ws-gateway/internal/link"
// 	"github.com/gotomicro/ego/core/elog"
// 	clientv3 "go.etcd.io/etcd/client/v3"
// )
//
// // GatewayService 网关服务，集成了服务注册中心和连接管理
// type GatewayService struct {
// 	registry    gateway.ServiceRegistry
// 	linkManager gateway.LinkManager
// 	nodeInfo    *apiv1.Node
// 	leaseID     clientv3.LeaseID
// 	logger      *elog.Component
// }
//
// // NewGatewayService 创建一个集成服务注册中心的网关服务
// func NewGatewayService(etcdClient *clientv3.Client, linkManager gateway.LinkManager, nodeInfo *apiv1.Node) *GatewayService {
// 	return &GatewayService{
// 		registry:    NewEtcdRegistry(etcdClient),
// 		linkManager: linkManager,
// 		nodeInfo:    nodeInfo,
// 		logger:      elog.EgoLogger.With(elog.FieldComponent("GatewayService")),
// 	}
// }
//
// // Start 启动网关服务，包括节点注册、续期和负载上报
// func (gs *GatewayService) Start(ctx context.Context) error {
// 	gs.logger.Info("启动网关服务",
// 		elog.String("nodeID", gs.nodeInfo.GetId()),
// 		elog.String("nodeIP", gs.nodeInfo.GetIp()),
// 		elog.Int32("nodePort", gs.nodeInfo.GetPort()))
//
// 	// 1. 注册节点到服务中心
// 	leaseID, err := gs.registry.Register(ctx, gs.nodeInfo)
// 	if err != nil {
// 		gs.logger.Error("注册节点失败", elog.FieldErr(err))
// 		return err
// 	}
// 	gs.leaseID = leaseID
//
// 	// 2. 启动租约续期
// 	gs.registry.KeepAlive(ctx, leaseID)
//
// 	// 3. 启动负载上报
// 	loadReporter := func() int64 {
// 		return gs.linkManager.Len() // 返回当前连接数作为负载
// 	}
// 	if err := gs.registry.StartNodeStateUpdater(ctx, leaseID, gs.nodeInfo.GetId(), loadReporter, 30*time.Second); err != nil {
// 		gs.logger.Error("启动负载上报失败", elog.FieldErr(err))
// 		return err
// 	}
//
// 	gs.logger.Info("网关服务启动成功",
// 		elog.String("nodeID", gs.nodeInfo.GetId()),
// 		elog.Int64("leaseID", int64(leaseID)))
//
// 	return nil
// }
//
// // GracefulShutdown 优雅关闭网关服务
// func (gs *GatewayService) GracefulShutdown(ctx context.Context) error {
// 	gs.logger.Info("开始优雅关闭网关服务",
// 		elog.String("nodeID", gs.nodeInfo.GetId()),
// 		elog.Int64("currentConnections", gs.linkManager.Len()))
//
// 	// 1. 获取其他可用节点用于连接重定向
// 	availableNodes, err := gs.registry.GetAvailableNodes(ctx, gs.nodeInfo.GetId())
// 	if err != nil {
// 		gs.logger.Warn("获取可用节点失败，跳过连接重定向", elog.FieldErr(err))
// 	} else if len(availableNodes) > 0 {
// 		// 2. 重定向所有连接到其他节点
// 		nodeList := &apiv1.NodeList{Nodes: availableNodes}
// 		selector := &link.AllLinksSelector{} // 选择所有连接进行重定向
//
// 		if err := gs.linkManager.RedirectLinks(ctx, selector, nodeList); err != nil {
// 			gs.logger.Warn("重定向连接失败", elog.FieldErr(err))
// 		} else {
// 			gs.logger.Info("连接重定向完成",
// 				elog.Int("availableNodeCount", len(availableNodes)))
// 		}
// 	} else {
// 		gs.logger.Warn("没有可用节点进行连接重定向")
// 	}
//
// 	// 3. 优雅注销节点（先降权重，等待，再删除）
// 	if err := gs.registry.GracefulDeregister(ctx, gs.leaseID, gs.nodeInfo.GetId()); err != nil {
// 		gs.logger.Error("优雅注销节点失败", elog.FieldErr(err))
// 		// 即使注销失败也要继续关闭连接
// 	}
//
// 	// 4. 优雅关闭所有连接
// 	if err := gs.linkManager.GracefulClose(ctx); err != nil {
// 		gs.logger.Error("优雅关闭连接失败", elog.FieldErr(err))
// 		return err
// 	}
//
// 	gs.logger.Info("网关服务优雅关闭完成")
// 	return nil
// }
//
// // ForceShutdown 强制关闭网关服务
// func (gs *GatewayService) ForceShutdown(ctx context.Context) error {
// 	gs.logger.Warn("强制关闭网关服务",
// 		elog.String("nodeID", gs.nodeInfo.GetId()))
//
// 	// 1. 立即注销节点
// 	if err := gs.registry.Deregister(ctx, gs.leaseID, gs.nodeInfo.GetId()); err != nil {
// 		gs.logger.Error("强制注销节点失败", elog.FieldErr(err))
// 	}
//
// 	// 2. 强制关闭所有连接
// 	if err := gs.linkManager.Close(); err != nil {
// 		gs.logger.Error("强制关闭连接失败", elog.FieldErr(err))
// 		return err
// 	}
//
// 	gs.logger.Info("网关服务强制关闭完成")
// 	return nil
// }
//
// // RunWithGracefulShutdown 运行网关服务并支持优雅关闭
// // 这是一个完整的示例，展示如何集成服务注册中心和连接管理
// func RunWithGracefulShutdown() {
// 	// 创建上下文，用于控制服务生命周期
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()
//
// 	// 1. 创建 etcd 客户端
// 	etcdClient, err := clientv3.New(clientv3.Config{
// 		Endpoints:   []string{"localhost:2379"},
// 		DialTimeout: 5 * time.Second,
// 	})
// 	if err != nil {
// 		elog.EgoLogger.Fatal("创建etcd客户端失败", elog.FieldErr(err))
// 	}
// 	defer etcdClient.Close()
//
// 	// 2. 创建连接管理器
// 	linkManager := link.NewManager(nil, nil) // 这里需要传入实际的codec和config
//
// 	// 3. 准备节点信息
// 	nodeInfo := &apiv1.Node{
// 		Id:       "gw-node-1",
// 		Ip:       "192.168.1.100",
// 		Port:     8080,
// 		Weight:   100,
// 		Location: "cn-beijing",
// 		Labels:   []string{"gateway", "websocket"},
// 		Capacity: 10000,
// 		Load:     0,
// 	}
//
// 	// 4. 创建网关服务
// 	gatewayService := NewGatewayService(etcdClient, linkManager, nodeInfo)
//
// 	// 5. 启动网关服务
// 	if err := gatewayService.Start(ctx); err != nil {
// 		elog.EgoLogger.Fatal("启动网关服务失败", elog.FieldErr(err))
// 	}
//
// 	// 6. 监听退出信号
// 	signalChan := make(chan os.Signal, 1)
// 	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
//
// 	// 7. 等待退出信号
// 	<-signalChan
// 	elog.EgoLogger.Info("收到退出信号，开始优雅关闭")
//
// 	// 8. 创建关闭超时上下文
// 	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
// 	defer shutdownCancel()
//
// 	// 9. 优雅关闭服务
// 	if err := gatewayService.GracefulShutdown(shutdownCtx); err != nil {
// 		elog.EgoLogger.Error("优雅关闭失败，尝试强制关闭", elog.FieldErr(err))
//
// 		// 如果优雅关闭失败，尝试强制关闭
// 		forceCtx, forceCancel := context.WithTimeout(context.Background(), 5*time.Second)
// 		defer forceCancel()
//
// 		if err := gatewayService.ForceShutdown(forceCtx); err != nil {
// 			elog.EgoLogger.Error("强制关闭也失败", elog.FieldErr(err))
// 		}
// 	}
//
// 	elog.EgoLogger.Info("程序退出")
// }
//
// // 负载均衡选择器示例
// type WeightedNodeSelector struct{}
//
// // SelectNode 基于权重选择最合适的节点
// func (s *WeightedNodeSelector) SelectNode(nodes []*apiv1.Node) *apiv1.Node {
// 	if len(nodes) == 0 {
// 		return nil
// 	}
//
// 	// 过滤掉权重为0的节点（准备关闭的节点）
// 	var availableNodes []*apiv1.Node
// 	for _, node := range nodes {
// 		if node.Weight > 0 {
// 			availableNodes = append(availableNodes, node)
// 		}
// 	}
//
// 	if len(availableNodes) == 0 {
// 		return nil
// 	}
//
// 	// 简单选择权重最高且负载最低的节点
// 	bestNode := availableNodes[0]
// 	bestScore := float64(bestNode.Weight) / float64(bestNode.Load+1) // +1避免除零
//
// 	for _, node := range availableNodes[1:] {
// 		score := float64(node.Weight) / float64(node.Load+1)
// 		if score > bestScore {
// 			bestScore = score
// 			bestNode = node
// 		}
// 	}
//
// 	return bestNode
// }
