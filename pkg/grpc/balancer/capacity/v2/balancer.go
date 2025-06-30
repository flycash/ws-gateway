// Package v2 实现一个"容量递增 + 轮询"LB。
// 每条后端在 resolver.Address.Attributes 中带 4 个字段：
//
//	initCapacity   int64   初始容量
//	maxCapacity    int64   最大容量
//	increaseStep   int64   每秒线性递增步长（若为 0，则用 growthRate）
//	growthRate     float64 每秒按比例递增(0.1 代表 +10%)；二选一
//
// 十分适合做"灰度逐步放量"。
package v2

import (
	"sync"

	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/resolver"
)

const (
	BalancerName = "capacity_round_robin_v2"
)

// NewBuilder 创建一个新的自定义 Builder。
//
// 为何需要自定义 Builder 而不使用 `base.NewBalancerBuilder`?
// `base.NewBalancerBuilder` 会在所有服务间共享同一个 `PickerBuilder` 实例，这会导致严重的服务间状态污染问题。
// (详见 v1 版本的注释)。
//
// 通过实现自定义的 `balancer.Builder` 接口，我们可以在其 `Build` 方法中，为每一个新的 `ClientConn`
// (即每一个新的服务连接) 创建一个全新的、独立的 `capacityBalancer` 和 `pickerBuilder` 实例。
// 这从根本上保证了每个服务之间的负载均衡状态是完全隔离的，互不干扰。
func NewBuilder() balancer.Builder {
	builder := &balancerBuilder{}
	balancer.Register(builder)
	return builder
}

// balancerBuilder 是一个实现了 `balancer.Builder` 接口的空结构体，其唯一目的是构建 `capacityBalancer`。
type balancerBuilder struct{}

// Build 为每个 ClientConn 创建一个全新的、隔离的 Balancer 实例。
func (b *balancerBuilder) Build(cc balancer.ClientConn, _ balancer.BuildOptions) balancer.Balancer {
	return &capacityBalancer{
		conn:          cc,
		pickerBuilder: &pickerBuilder{}, // 每个 Balancer 实例都拥有一个独立的、有状态的 pickerBuilder。
		subConns:      make(map[balancer.SubConn]*subConnInfo),
	}
}

func (b *balancerBuilder) Name() string {
	return BalancerName
}

// capacityBalancer 是自定义的 Balancer 实现。
//
// 核心职责: SubConn 生命周期管理器。
// 1. 接收来自 Resolver 的权威地址列表。
// 2. 计算地址变更，创建 (NewSubConn) 或销毁 (Shutdown) SubConn。
// 3. 监听所有 SubConn 的连接状态变化。
// 4. 当一个 SubConn 被确认永久删除时，负责通知 pickerBuilder 清理其长期保留的状态。
// 5. 在任何可能影响可用连接集合的事件发生后，调用 pickerBuilder 来构建一个新的 Picker，并更新给 gRPC 内核。
//
// 它自身是无状态的（就容量而言），所有与容量相关的、需要跨时间保持的状态，都委托给 pickerBuilder 管理。
type capacityBalancer struct {
	conn          balancer.ClientConn
	pickerBuilder *pickerBuilder
	mu            sync.RWMutex
	subConns      map[balancer.SubConn]*subConnInfo
}

type subConnInfo struct {
	addr  resolver.Address
	state connectivity.State
}

// UpdateClientConnState 处理连接状态更新
func (b *capacityBalancer) UpdateClientConnState(state balancer.ClientConnState) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 计算新旧地址的差异
	addrsSet := make(map[string]bool)
	for _, addr := range state.ResolverState.Addresses {
		addrsSet[addr.Addr] = true
	}

	// 移除不再需要的 SubConn
	for sc, info := range b.subConns {
		if !addrsSet[info.addr.Addr] {
			sc.Shutdown()
			delete(b.subConns, sc)

			// 通知 pickerBuilder 清理过期的状态
			nodeID := attrAs(info.addr, attrNodeID, "")
			if nodeID != "" {
				b.pickerBuilder.RemoveSubConn(nodeID)
			}
		}
	}

	// 为新地址创建 SubConn
	for _, addr := range state.ResolverState.Addresses {
		found := false
		for _, info := range b.subConns {
			if info.addr.Addr == addr.Addr {
				found = true
				break
			}
		}
		if found {
			continue
		}

		// 创建SubConn，使用闭包方式实现StateListener
		var sc balancer.SubConn
		var err error
		sc, err = b.conn.NewSubConn([]resolver.Address{addr}, balancer.NewSubConnOptions{
			StateListener: func(state balancer.SubConnState) {
				// 使用闭包捕获的sc变量，调用统一的状态处理方法
				b.handleSubConnStateChange(sc, state)
			},
		})
		if err != nil {
			continue
		}

		b.subConns[sc] = &subConnInfo{
			addr:  addr,
			state: connectivity.Idle,
		}
		sc.Connect()
	}

	// 更新 Picker
	b.updatePicker()
	return nil
}

// updatePicker 更新 Picker 状态
func (b *capacityBalancer) updatePicker() {
	// 收集所有 Ready 状态的连接
	readySCs := make(map[balancer.SubConn]base.SubConnInfo)
	hasReady := false

	for sc, info := range b.subConns {
		if info.state == connectivity.Ready {
			readySCs[sc] = base.SubConnInfo{
				Address: info.addr,
			}
			hasReady = true
		}
	}

	if !hasReady {
		// 没有可用连接
		b.conn.UpdateState(balancer.State{
			ConnectivityState: connectivity.TransientFailure,
			Picker:            base.NewErrPicker(balancer.ErrNoSubConnAvailable),
		})
		return
	}

	// 使用独立的 pickerBuilder 构建 picker
	picker := b.pickerBuilder.Build(base.PickerBuildInfo{
		ReadySCs: readySCs,
	})

	b.conn.UpdateState(balancer.State{
		ConnectivityState: connectivity.Ready,
		Picker:            picker,
	})
}

// ResolverError 处理 Resolver 错误
func (b *capacityBalancer) ResolverError(err error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(b.subConns) == 0 {
		b.conn.UpdateState(balancer.State{
			ConnectivityState: connectivity.TransientFailure,
			Picker:            base.NewErrPicker(err),
		})
	}
}

// handleSubConnStateChange 处理 SubConn 状态变更的核心逻辑
func (b *capacityBalancer) handleSubConnStateChange(sc balancer.SubConn, state balancer.SubConnState) {
	b.mu.Lock()
	defer b.mu.Unlock()

	info, exists := b.subConns[sc]
	if !exists {
		return
	}

	oldState := info.state
	newState := state.ConnectivityState

	// 更新状态
	info.state = newState

	// 处理特定状态的业务逻辑
	switch newState {
	case connectivity.Idle:
		// 空闲状态，无需特殊处理
	case connectivity.Connecting:
		// 连接中状态，无需特殊处理
	case connectivity.Ready:
		// 就绪状态，无需特殊处理
	case connectivity.TransientFailure:
		// 连接失败时重连
		sc.Connect()
	case connectivity.Shutdown:
		// 连接关闭时清理
		delete(b.subConns, sc)
	}

	// 精细化控制：只在影响可用连接集合时才调用updatePicker
	if b.shouldUpdatePicker(oldState, newState) {
		b.updatePicker()
	}
}

// shouldUpdatePicker 判断是否需要重建Picker
func (b *capacityBalancer) shouldUpdatePicker(oldState, newState connectivity.State) bool {
	// 状态没有变化
	if oldState == newState {
		return false
	}

	// 情况1：从Ready变为非Ready - 失去可用连接
	if oldState == connectivity.Ready && newState != connectivity.Ready {
		return true
	}

	// 情况2：从非Ready变为Ready - 新增可用连接
	if oldState != connectivity.Ready && newState == connectivity.Ready {
		return true
	}

	// 情况3：Shutdown状态 - 连接被移除
	if newState == connectivity.Shutdown {
		return true
	}

	// 其他情况：非Ready状态之间的转换，不影响可用连接集合
	return false
}

// UpdateSubConnState 处理 SubConn 状态更新（已废弃的接口方法，但仍需实现）
func (b *capacityBalancer) UpdateSubConnState(sc balancer.SubConn, state balancer.SubConnState) {
	// 委托给统一的处理方法
	b.handleSubConnStateChange(sc, state)
}

// Close 关闭 Balancer
func (b *capacityBalancer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for sc := range b.subConns {
		sc.Shutdown()
	}
	b.subConns = nil
}
