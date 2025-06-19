package webhook

import (
	"fmt"

	"gitee.com/flycash/ws-gateway/internal/link"
	"github.com/ecodeclub/ginx"
	"github.com/gotomicro/ego/core/elog"
	"github.com/gotomicro/ego/server/egin"
)

type Handler struct {
	svc              *Service
	rebalancePercent float64
	logger           *elog.Component
}

func NewHandler(svc *Service, rebalancePercent float64) *Handler {
	return &Handler{
		svc:              svc,
		rebalancePercent: rebalancePercent,
		logger:           elog.EgoLogger.With(elog.FieldComponent("Webhook.Handler")),
	}
}

// PrivateAPI 是指需要登录验证的.
func (h *Handler) PrivateAPI(_ *egin.Component) {}

// PublicAPI 是指不需要登录验证的
func (h *Handler) PublicAPI(server *egin.Component) {
	g := server.Group("/webhook")
	g.POST("/v1", ginx.B[GrafanaWebhookPayload](h.Rebalance))
	g.POST("/v2", ginx.B[GrafanaWebhookPayload](h.ScaleUp))
}

// Rebalance 演示网关节点收到告警后，智能再均衡
func (h *Handler) Rebalance(ctx *ginx.Context, req GrafanaWebhookPayload) (ginx.Result, error) {
	h.logger.Info("智能再均衡，收到参数",
		elog.Any("req", req),
	)

	// 过滤 DatasourceNoData 告警，避免无意义的触发
	if h.isDatasourceNoDataAlert(req) {
		h.logger.Warn("收到 DatasourceNoData 告警，已忽略", elog.String("status", req.Status))
		return ginx.Result{Msg: "OK"}, nil
	}

	h.logger.Info("收到 Grafana webhook 回调",
		elog.String("status", req.Status))

	// 迁移当前连接数的百分比
	linkSelector := link.NewPercentLinksSelector(h.rebalancePercent)
	err := h.svc.Rebalance(ctx, linkSelector)
	if err != nil {
		h.logger.Error("智能再均衡失败",
			elog.Any("req", req),
			elog.FieldErr(err),
		)
		return ginx.Result{}, fmt.Errorf("智能再均衡失败: %w", err)
	}

	h.logger.Info("智能再均衡成功")
	return ginx.Result{
		Msg: "OK",
	}, nil
}

// ScaleUp 演示网关节点收到告警后，扩容再均衡
func (h *Handler) ScaleUp(ctx *ginx.Context, req GrafanaWebhookPayload) (ginx.Result, error) {
	h.logger.Info("扩容再均衡，收到参数",
		elog.Any("req", req),
	)

	// 过滤 DatasourceNoData 告警，避免无意义的触发
	if h.isDatasourceNoDataAlert(req) {
		h.logger.Warn("收到 DatasourceNoData 告警，已忽略", elog.String("status", req.Status))
		return ginx.Result{Msg: "OK"}, nil
	}

	h.logger.Info("收到 Grafana webhook 回调",
		elog.String("status", req.Status))

	err := h.svc.ScaleUp(ctx)
	if err != nil {
		h.logger.Error("扩容再均衡失败",
			elog.Any("req", req),
			elog.FieldErr(err),
		)
		return ginx.Result{}, fmt.Errorf("扩容再均衡失败: %w", err)
	}

	h.logger.Info("扩容再均衡成功")
	return ginx.Result{
		Msg: "OK",
	}, nil
}

// isDatasourceNoDataAlert 检查是否为数据源无数据告警
func (h *Handler) isDatasourceNoDataAlert(req GrafanaWebhookPayload) bool {
	for i := range req.Alerts {
		if req.Alerts[i].AlertName() == "DatasourceNoData" {
			return true
		}
	}
	return false
}
