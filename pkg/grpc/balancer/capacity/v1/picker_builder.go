package v1

import (
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/resolver"
)

type pickerBuilder struct {
	picker *picker
}

func (b *pickerBuilder) Build(info base.PickerBuildInfo) balancer.Picker {
	// 构建新的连接映射
	newConns := make(map[string]*subConn)
	connSlice := make([]*subConn, 0, len(info.ReadySCs))

	for con, conInfo := range info.ReadySCs {
		nodeID := attrAs(conInfo.Address, attrNodeID, "")
		if nodeID == "" {
			continue // 跳过无效节点
		}
		// 获取单个节点的连接
		sc := b.getSubConn(nodeID, con, conInfo)
		if sc != nil {
			newConns[nodeID] = sc
			connSlice = append(connSlice, sc)
		}
	}

	// 关闭已删除的连接
	if b.picker != nil {
		for nodeID, oldConn := range b.picker.conns {
			if newSc, exists := newConns[nodeID]; !exists {
				// 节点已被删除，关闭连接
				oldConn.SubConn.Shutdown()
			} else if newSc != oldConn {
				// 如果不是同一个subConn实例，说明是地址变更，也要关闭旧连接
				oldConn.SubConn.Shutdown()
			}
		}
	}

	p := &picker{
		conns:     newConns,
		connSlice: connSlice,
		length:    int64(len(connSlice)),
	}
	p.index.Store(-1)
	b.picker = p
	return p
}

// getSubConn 获取单个节点的连接
func (b *pickerBuilder) getSubConn(nodeID string, con balancer.SubConn, conInfo base.SubConnInfo) *subConn {
	// 检查是否是完全相同的节点（NodeID + Address都相同）
	var old *subConn
	if b.picker != nil {
		old = b.picker.conns[nodeID]
	}
	needCreate := old == nil || old.addr.Addr != conInfo.Address.Addr
	if needCreate {
		// 情况1: 全新节点
		// 情况2: NodeID相同但地址变了 - 创建新连接，旧连接稍后统一关闭
		return b.createNewSubConn(nodeID, con, conInfo)
	}
	// 情况3: NodeID和地址都相同 - 真正的复用
	return b.reuseSubConn(old, con, conInfo)
}

// createNewSubConn 创建新的subConn（全新节点或地址变更）
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

func attrAs[T float64 | int64 | string](ra resolver.Address, key string, def T) T {
	if v, ok := ra.Attributes.Value(key).(T); ok {
		return v
	}
	return def
}

// reuseSubConn 复用现有的subConn
func (b *pickerBuilder) reuseSubConn(old *subConn, con balancer.SubConn, conInfo base.SubConnInfo) *subConn {
	// 更新SubConn引用（可能是重连后的新SubConn）
	old.SubConn = con
	// 只更新配置，保持当前容量
	old.increaseStep = attrAs(conInfo.Address, attrStep, int64(0))
	old.growthRate = attrAs(conInfo.Address, attrRate, defaultGrowthRate)
	old.maxCapacity.Store(attrAs(conInfo.Address, attrMaxCap, int64(defaultMaxCapacity)))
	return old
}
