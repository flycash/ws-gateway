package v1

import (
	"crypto/rand"
	"math/big"
	"sync/atomic"

	"google.golang.org/grpc/balancer"
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
