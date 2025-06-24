package v2

import (
	"crypto/rand"
	"math/big"
	"sync/atomic"

	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/resolver"
)

const (
	// resolver.Address.Attributes 中定义的元数据 key
	attrInitCap = "initCapacity" // 初始容量
	attrMaxCap  = "maxCapacity"  // 最大容量
	attrStep    = "increaseStep" // 线性增长步长
	attrRate    = "growthRate"   // 比例增长率
	attrNodeID  = "nodeID"       // 节点的唯一标识符

	// 默认容量配置
	defaultGrowthRate   = 0.1 // 默认增长率 10%
	defaultInitCapacity = 10  // 默认初始容量
	defaultMaxCapacity  = 100 // 默认最大容量

	// 选择算法配置
	maxRetryMultiplier = 2 // 在降级为简单轮询前，对部分容量节点进行随机选择的最大尝试次数（乘以节点数）
)

// subConn 是对 `balancer.SubConn` 的包装，附加了与容量相关的状态和配置。
// 它的实例由 pickerBuilder 创建和管理，picker 仅持有其引用。
type subConn struct {
	balancer.SubConn
	addr resolver.Address
	id   string

	curCapacity atomic.Int64 // 当前容量，原子操作保证并发安全
	maxCapacity atomic.Int64 // 最大容量，原子操作以支持动态更新
	// 哪种增长的快用哪个
	increaseStep int64   // 线性增长
	growthRate   float64 // 比例增长
}

// picker 是 gRPC 负载均衡的核心选择器。
//
// 核心职责:
//  1. 在每次 RPC 调用时，执行 `Pick` 方法，从可用连接列表中选择一个 `SubConn`。
//  2. 它是无状态、一次性的。它的所有状态 (可用连接列表 `conns`) 都在构建时由 `pickerBuilder` 一次性注入。
//     它自身不修改这个列表。
//
// 选择策略 (混合策略):
//  1. 基础轮询: 每次选择从下一个节点开始，保证选择机会均等。
//  2. 容量优先: 如果轮询到的节点已满容，直接选中，实现最高效的利用。
//  3. 容量感知随机: 如果节点未满容，则根据其当前容量 `curCapacity` 与最大容量 `maxCapacity` 的比例，
//     进行一次随机判断。这使得容量越大的节点越容易被选中，实现按容量的加权选择。
//  4. 失败降级: 如果经过多次尝试（例如，所有节点容量都很低，随机选择均未命中），为了保证服务可用性，
//     会降级为简单的轮询选择，直接选定一个节点返回，避免无限重试或无连接可选。
//
// Done 回调:
// 在 `Pick` 方法返回的 `Done` 回调中，如果 RPC 调用成功，会调用 `increaseCapacity` 方法来增加被选中节点的容量，
// 从而实现容量的动态增长。
type picker struct {
	conns  []*subConn
	index  atomic.Int64
	length int64
}

func (p *picker) Pick(_ balancer.PickInfo) (balancer.PickResult, error) {
	if len(p.conns) == 0 {
		return balancer.PickResult{}, balancer.ErrNoSubConnAvailable
	}

	var selected *subConn
	attempts := 0
	maxAttempts := len(p.conns) * maxRetryMultiplier // 避免无限循环

	for attempts < maxAttempts {
		// 轮询选择节点
		idx := p.index.Add(1)
		conn := p.conns[int(idx%p.length)]

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
		selected = p.conns[int(idx%p.length)]
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
