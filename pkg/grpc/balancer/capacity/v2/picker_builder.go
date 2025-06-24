package v2

import (
	"sync"

	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/resolver"
)

// pickerBuilder 是一个有状态的 Picker 工厂。
//
// 核心职责:
//  1. 持久化状态: 维护一个 `subConnStates` map 作为"长期记忆"，在多次 Build 调用之间跟踪每个节点的容量信息。
//     这是解决"短暂断连导致容量重置"问题的关键。
//  2. 构建 Picker: 每次被 Balancer 调用时，根据传入的 `ReadySCs` 和自身的"长期记忆"，构建一个全新的、
//     一次性的 `picker` 实例。
//  3. 响应清理: 提供 `RemoveSubConn` 方法，允许 Balancer 在节点被永久移除时，清理其持久化的状态，防止内存泄漏。
//
// 设计陷阱与演进:
// 此实现规避了一个常见陷阱：直接用 `ReadySCs` 构建的新状态去覆盖旧的完整状态。那种做法会导致短暂离线的节点
// (例如，因网络抖动而临时进入 Connecting 状态) 的容量信息丢失。
// 当前的设计通过只对 `subConnStates` 进行增、改和（按需）删的操作，而不是整体替换，保证了状态的持久性和正确性。
type pickerBuilder struct {
	// subConnStates 是 pickerBuilder 的核心，用于在多次 Build 调用之间持久化每个节点的容量信息
	// key 是 nodeID
	subConnStates map[string]*subConn
	mu            sync.RWMutex
}

func (b *pickerBuilder) Build(info base.PickerBuildInfo) balancer.Picker {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 第一次调用时初始化"长期记忆" map
	if b.subConnStates == nil {
		b.subConnStates = make(map[string]*subConn)
	}

	activeConns := make([]*subConn, 0, len(info.ReadySCs))

	for con, conInfo := range info.ReadySCs {
		nodeID := attrAs(conInfo.Address, attrNodeID, "")
		if nodeID == "" {
			continue
		}

		// 核心逻辑: 复用或创建带有容量状态的 subConn
		if oldSc, ok := b.subConnStates[nodeID]; ok && oldSc.addr.Addr == conInfo.Address.Addr {
			// 情况1: 节点未变（包括因网络问题短暂断连后恢复），复用旧的 subConn 以保留容量状态
			oldSc.SubConn = con // 更新底层的 SubConn 引用
			// 关键修复: 使用最新的配置更新节点属性
			b.updateAttributes(oldSc, conInfo)
			activeConns = append(activeConns, oldSc)
		} else {
			// 情况2: 全新节点，或者节点地址发生变更，创建新的 subConn
			sc := b.createNewSubConn(nodeID, con, conInfo)
			b.subConnStates[nodeID] = sc // 将新状态存入长期记忆
			activeConns = append(activeConns, sc)
		}
	}

	return &picker{
		conns:  activeConns,
		length: int64(len(activeConns)),
	}
}

// updateAttributes 使用最新的 conInfo 更新一个现有的 subConn 状态。
//
// 这是实现"动态配置更新"的关键。当一个已存在的节点其配置 (如 maxCapacity) 在服务注册中心被修改时，
// resolver 会通知 balancer，最终传递到这里。此方法确保了这些更新能够实时生效，而不会被旧的状态覆盖。
func (b *pickerBuilder) updateAttributes(sc *subConn, conInfo base.SubConnInfo) {
	// 只更新可动态调整的配置，保持 curCapacity 不变
	sc.increaseStep = attrAs(conInfo.Address, attrStep, int64(0))
	sc.growthRate = attrAs(conInfo.Address, attrRate, defaultGrowthRate)
	sc.maxCapacity.Store(attrAs(conInfo.Address, attrMaxCap, int64(defaultMaxCapacity)))
}

// createNewSubConn 创建新的subConn（全新节点或地址变更）
// 注意：这个方法现在是 pickerBuilder 的一部分
func (b *pickerBuilder) createNewSubConn(nodeID string, con balancer.SubConn, conInfo base.SubConnInfo) *subConn {
	sc := &subConn{
		SubConn:      con,
		addr:         conInfo.Address,
		id:           nodeID,
		increaseStep: attrAs(conInfo.Address, attrStep, int64(0)),
		growthRate:   attrAs(conInfo.Address, attrRate, defaultGrowthRate),
	}

	// 无论是全新节点还是地址变更，都使用初始容量重新开始
	sc.curCapacity.Store(attrAs(conInfo.Address, attrInitCap, int64(defaultInitCapacity)))
	sc.maxCapacity.Store(attrAs(conInfo.Address, attrMaxCap, int64(defaultMaxCapacity)))
	return sc
}

// RemoveSubConn 由 Balancer 调用，用于在节点被永久删除时清理其状态，防止内存泄漏。
func (b *pickerBuilder) RemoveSubConn(nodeID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subConnStates, nodeID)
}

func attrAs[T float64 | int64 | string](ra resolver.Address, key string, def T) T {
	if v, ok := ra.Attributes.Value(key).(T); ok {
		return v
	}
	return def
}
