package webhook

import (
	"context"
	"fmt"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/event"
	"gitee.com/flycash/ws-gateway/pkg/scaler"
	"github.com/gotomicro/ego/core/elog"
)

type Service struct {
	nodeInfo    *apiv1.Node
	registry    gateway.ServiceRegistry
	linkManager gateway.LinkManager
	producer    event.ScaleUpEventProducer
	scaler      scaler.Scaler
	logger      *elog.Component
}

func NewService(nodeInfo *apiv1.Node,
	registry gateway.ServiceRegistry,
	linkManager gateway.LinkManager,
	producer event.ScaleUpEventProducer,
	scaler scaler.Scaler,
) *Service {
	return &Service{
		nodeInfo:    nodeInfo,
		registry:    registry,
		linkManager: linkManager,
		producer:    producer,
		scaler:      scaler,
		logger:      elog.EgoLogger.With(elog.FieldComponent("webhook.Service")),
	}
}

func (s *Service) Rebalance(ctx context.Context, selector gateway.LinkSelector) error {
	s.logger.Info("接收到智能重均衡指令，开始重定向客户端")

	// 获取其他可用的健康节点
	availableNodes, err := s.registry.GetAvailableNodes(ctx, s.nodeInfo.GetId())
	if err != nil {
		s.logger.Error("重均衡失败：获取可用节点列表失败", elog.FieldErr(err))
		return fmt.Errorf("获取可用节点失败: %w", err)
	}

	s.logger.Info("找到可用重定向节点", elog.Any("nodes", availableNodes))

	// 对选中的link集合进行重定向
	err = s.linkManager.RedirectLinks(ctx, selector, &apiv1.NodeList{Nodes: availableNodes})
	if err != nil {
		s.logger.Error("重均衡失败：下发重定向指令时出错", elog.FieldErr(err))
		return err
	}
	s.logger.Info("成功向部分客户端下发重定向指令")
	return nil
}

func (s *Service) ScaleUp(ctx context.Context) error {
	s.logger.Info("开始扩容流程")

	// 调用DockerScaler进行完整扩容操作（包括等待注册成功）
	scaleUpCount := 1
	newNodes, err := s.scaler.ScaleUp(ctx, scaleUpCount)
	if err != nil {
		s.logger.Error("扩容操作失败", elog.FieldErr(err))
		return fmt.Errorf("扩容操作失败: %w", err)
	}

	if newNodes == nil || len(newNodes.Nodes) == 0 {
		s.logger.Warn("扩容操作没有创建新节点，可能存在并发扩容或其他原因")
		return nil
	}

	s.logger.Info("扩容操作成功，新节点已注册到服务中心",
		elog.Int("newNodeCount", len(newNodes.Nodes)))

	// 获取最新的可用节点列表用于计算总数
	availableNodes, err := s.registry.GetAvailableNodes(ctx, s.nodeInfo.GetId())
	if err != nil {
		s.logger.Error("获取可用节点列表失败", elog.FieldErr(err))
		return fmt.Errorf("获取可用节点列表失败: %w", err)
	}

	// 发送扩容事件通知集群中的其他节点
	totalNodes := len(newNodes.Nodes) + len(availableNodes) + 1 // +1是当前节点
	scaleUpEvent := event.ScaleUpEvent{
		NewNodeCount:   int64(len(newNodes.Nodes)),
		TotalNodeCount: int64(totalNodes),
		NewNodeList:    newNodes,
	}
	err = s.producer.Produce(ctx, scaleUpEvent)
	if err != nil {
		s.logger.Error("发送扩容事件失败", elog.FieldErr(err))
		return fmt.Errorf("发送扩容事件失败: %w", err)
	}

	s.logger.Info("扩容流程完成",
		elog.Int("newNodeCount", len(newNodes.Nodes)),
		elog.Int("totalNodeCount", totalNodes))

	return nil
}
