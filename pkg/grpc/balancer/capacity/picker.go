package capacity

import (
	"crypto/rand"
	"math/big"
	"sync/atomic"

	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/resolver"
)

const (
	attrInitCap = "initCapacity"
	attrMaxCap  = "maxCapacity"
	attrStep    = "increaseStep"
	attrRate    = "growthRate"
	attrNodeID  = "nodeID"

	// 默认容量配置
	defaultGrowthRate   = 0.1
	defaultInitCapacity = 10
	defaultMaxCapacity  = 100

	// 选择算法配置
	maxRetryMultiplier = 2
)

type subConn struct {
	balancer.SubConn
	addr resolver.Address
	id   string

	curCapacity atomic.Int64 // 当前容量
	maxCapacity atomic.Int64 // 最大容量
	// 哪种增长的快用哪个
	increaseStep int64   // 线性增长
	growthRate   float64 // 比例增长
}

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

type picker struct {
	conns     map[string]*subConn // key: nodeID, value: subConn (用于差值计算)
	connSlice []*subConn          // 用于轮询的固定顺序slice
	index     atomic.Int64
	length    int64
}

func (p *picker) Pick(_ balancer.PickInfo) (balancer.PickResult, error) {
	if len(p.connSlice) == 0 {
		return balancer.PickResult{}, balancer.ErrNoSubConnAvailable
	}

	var selected *subConn
	attempts := 0
	maxAttempts := len(p.connSlice) * maxRetryMultiplier // 避免无限循环

	for attempts < maxAttempts {
		// 轮询选择节点
		idx := p.index.Add(1)
		conn := p.connSlice[int(idx%p.length)]

		// 如果是满容量节点，直接选择
		if conn.curCapacity.Load() == conn.maxCapacity.Load() {
			selected = conn
			break
		}

		// 容量未满的节点，按比例随机选择
		curCap := conn.curCapacity.Load()
		maxCap := conn.maxCapacity.Load()
		if maxCap > 0 {
			// 使用加密安全的随机数生成器
			randomNum, err := rand.Int(rand.Reader, big.NewInt(maxCap))
			if err == nil && randomNum.Int64() < curCap {
				selected = conn
				break
			}
		}

		attempts++
	}

	// 如果经过多次尝试仍未选中，降级到简单轮询
	if selected == nil {
		idx := p.index.Add(1)
		selected = p.connSlice[int(idx%p.length)]
	}

	return balancer.PickResult{
		SubConn: selected.SubConn,
		Done: func(info balancer.DoneInfo) {
			// 成功且容量未满时才增长
			if info.Err == nil {
				p.increaseCapacity(selected)
			}
		},
	}, nil
}

// increaseCapacity 使用原子操作安全地增加容量
func (p *picker) increaseCapacity(conn *subConn) {
	for {
		oldCap := conn.curCapacity.Load()
		maxCap := conn.maxCapacity.Load()

		// 已达最大容量
		if oldCap >= maxCap {
			return
		}

		// 计算增长量
		delta := max(conn.increaseStep, int64(float64(oldCap)*conn.growthRate))
		newCap := min(oldCap+delta, maxCap)

		// 原子更新
		if conn.curCapacity.CompareAndSwap(oldCap, newCap) {
			return
		}
		// CAS失败，重试
	}
}
