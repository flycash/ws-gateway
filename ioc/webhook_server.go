package ioc

import (
	"log"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/event"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/internal/webhook"
	"gitee.com/flycash/ws-gateway/pkg/scaler"
	"github.com/gotomicro/ego/core/econf"
	"github.com/gotomicro/ego/server/egin"
)

func InitWebhookServer(nodeInfo *apiv1.Node,
	registry gateway.ServiceRegistry,
	linkManager *link.Manager,
	producer event.ScaleUpEventProducer,
	scaler scaler.Scaler,
) gateway.Server {
	rebalancePercent := econf.GetFloat64("server.webhook.rebalancePercent")
	log.Printf("rebalancePercent = %#v", rebalancePercent)
	server := egin.Load("server.webhook").Build()
	svc := webhook.NewService(nodeInfo, registry, linkManager, producer, scaler)
	h := webhook.NewHandler(svc, rebalancePercent)
	h.PublicAPI(server)
	return server
}
