package grpc

import (
	"fmt"
	"time"

	"gitee.com/flycash/ws-gateway/pkg/grpc/registry"
	"golang.org/x/net/context"
	"google.golang.org/grpc/attributes"
	"google.golang.org/grpc/resolver"
)

const (
	initCapacityStr = "initCapacity"
	maxCapacityStr  = "maxCapacity"
	increaseStepStr = "increaseStep"
	growthRateStr   = "growthRate"
	nodeIDStr       = "nodeID"
)

type resolverBuilder struct {
	r       registry.Registry
	timeout time.Duration
}

func NewResolverBuilder(r registry.Registry, timeout time.Duration) resolver.Builder {
	return &resolverBuilder{
		r:       r,
		timeout: timeout,
	}
}

func (r *resolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	res := &backendResolver{
		target:   target,
		cc:       cc,
		registry: r.r,
		close:    make(chan struct{}, 1),
		timeout:  r.timeout,
	}
	res.resolve()
	go res.watch()
	return res, nil
}

func (r *resolverBuilder) Scheme() string {
	return "backend"
}

type backendResolver struct {
	target   resolver.Target
	cc       resolver.ClientConn
	registry registry.Registry
	close    chan struct{}
	timeout  time.Duration
}

func (g *backendResolver) ResolveNow(_ resolver.ResolveNowOptions) {
	// 重新获取一下所有服务
	g.resolve()
}

func (g *backendResolver) Close() {
	g.close <- struct{}{}
}

func (g *backendResolver) watch() {
	events := g.registry.Subscribe(g.target.Endpoint())
	for {
		select {
		case <-events:
			g.resolve()

		case <-g.close:
			return
		}
	}
}

func (g *backendResolver) resolve() {
	serviceName := g.target.Endpoint()
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	instances, err := g.registry.ListServices(ctx, serviceName)
	cancel()
	if err != nil {
		g.cc.ReportError(err)
	}

	address := make([]resolver.Address, 0, len(instances))
	for _, ins := range instances {
		address = append(address, resolver.Address{
			Addr:       ins.Address,
			ServerName: ins.Name,
			Attributes: attributes.New(initCapacityStr, ins.InitCapacity).
				WithValue(maxCapacityStr, ins.MaxCapacity).
				WithValue(increaseStepStr, ins.IncreaseStep).
				WithValue(growthRateStr, ins.GrowthRate).
				WithValue(nodeIDStr, fmt.Sprintf("%s-%s", ins.Name, ins.ID)),
		})
	}
	err = g.cc.UpdateState(resolver.State{
		Addresses: address,
	})
	if err != nil {
		g.cc.ReportError(err)
	}
}
