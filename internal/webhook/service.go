package webhook

import (
	"context"
	"fmt"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/event"
	"github.com/gotomicro/ego/core/elog"
)

type Service struct {
	nodeInfo    *apiv1.Node
	registry    gateway.ServiceRegistry
	linkManager gateway.LinkManager
	producer    event.ScaleUpEventProducer
	logger      *elog.Component
}

func NewService(nodeInfo *apiv1.Node,
	registry gateway.ServiceRegistry,
	linkManager gateway.LinkManager,
	producer event.ScaleUpEventProducer,
) *Service {
	return &Service{
		nodeInfo:    nodeInfo,
		registry:    registry,
		linkManager: linkManager,
		producer:    producer,
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
	// 调用scaler
	s.logger.Info("扩容中....确保扩容节点正常启动 —— 拿到扩容节点在注册中心的信息")
	// todo:给出节点扩容的节点ID集合
	newNodeIDs := make([]string, 0)
	var availableNodes []*apiv1.Node
	newNodes := make([]*apiv1.Node, 0, len(newNodeIDs))
	// 获取所有可用节点信息
	// for len(newNodeIDs) != len(newNodes) {

	availableNodes, err := s.registry.GetAvailableNodes(ctx, s.nodeInfo.GetId())
	if err != nil {
		s.logger.Error("获取可用节点信息失败", elog.FieldErr(err))
		return fmt.Errorf("获取可用节点信息失败: %w", err)
	}

	for i := range newNodeIDs {
		for j := range availableNodes {
			if availableNodes[j].GetId() == newNodeIDs[i] {
				newNodes = append(newNodes, availableNodes[j])
			}
		}
	}

	// }
	nodeList := &apiv1.NodeList{Nodes: newNodes}
	return s.producer.Produce(ctx, event.ScaleUpEvent{
		NewNodeCount:   int64(len(newNodes)),
		TotalNodeCount: int64(len(availableNodes) + 1),
		NewNodeList:    nodeList,
	})
}
