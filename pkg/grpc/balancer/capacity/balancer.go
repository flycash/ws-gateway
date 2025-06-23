// Package capacity 实现一个“容量递增 + 轮询”LB。
// 每条后端在 resolver.Address.Attributes 中带 4 个字段：
//
//	initCapacity   int64   初始容量
//	maxCapacity    int64   最大容量
//	increaseStep   int64   每秒线性递增步长（若为 0，则用 growthRate）
//	growthRate     float64 每秒按比例递增(0.1 代表 +10%)；二选一
//
// 十分适合做“灰度逐步放量”。
package capacity

import (
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
)

const (
	BalancerName = "capacity_round_robin"
)

// NewBuilder ！！！警告：此函数包含一个严重的、会导致服务间状态交叉污染的 Bug。请勿在生产环境中使用。！！！
//
// 这个实现是 gRPC 自定义 Balancer 时最常见的一个陷阱。
// 它看起来很简洁，但其背后隐藏着致命的设计缺陷。
func NewBuilder() balancer.Builder {
	// BUG #1 (最致命): 状态在多个服务间被共享和污染
	// --------------------------------------------------------------------------------------------------
	// 问题根源: `&pickerBuilder{}` 这段代码在这里只会被执行一次。
	// 它创建了一个 `pickerBuilder` 的实例，并将其内存地址（指针）传递给了 `base.NewBalancerBuilder`。
	// `base.NewBalancerBuilder` 返回的 `builder` 对象会捕获并永久持有这个唯一的指针。
	//
	// 这意味着，无论我们通过这个 `builder` 为多少个不同的服务（即多少个 `grpc.NewClient` 调用）创建 Balancer，
	// 所有这些 Balancer 最终都会共享同一个 `pickerBuilder` 实例。
	//
	// 场景推演:
	// 1. `grpc.NewClient("Service-A")` 被调用。
	//    - gRPC 创建了一个 Balancer 实例 (我们称之为 bal_A)。
	//    - bal_A 内部引用了那个全局唯一的 `pickerBuilder` 实例。
	//    - 当 Service-A 的节点更新时，`pickerBuilder` 内部的 `picker` 字段被设置为 `picker_A`，其中包含了 Service-A 的节点信息。
	//
	// 2. `grpc.NewClient("Service-B")` 被调用。
	//    - gRPC 创建了另一个 Balancer 实例 (bal_B)，但它内部引用的还是那个全局唯一的 `pickerBuilder` 实例。
	//
	// 3. 灾难发生: 当 Service-B 的节点更新时，bal_B 开始工作。
	//    - 它调用 `pickerBuilder.Build` 方法，此时 `pickerBuilder.picker` 字段里存的是 `picker_A`！
	//    - 差值计算逻辑 (`for nodeID, oldConn := range b.picker.conns`) 会遍历 Service-A 的节点。
	//    - 它发现 Service-A 的所有节点都不在本次 Service-B 的更新列表里，于是得出结论：这些节点都“被删除了”。
	//    - bal_B 因此调用了 `oldConn.SubConn.Shutdown()`，将 Service-A 的所有连接都关闭了！
	//    - 最后，它用 `picker_B` 覆盖了 `pickerBuilder.picker` 字段，Service-A 的所有容量状态彻底丢失。
	//
	//
	// BUG #2: 潜在的并发安全问题
	// --------------------------------------------------------------------------------------------------
	// 虽然 gRPC 内部保证了对单个 Balancer 实例的方法调用是序列化的，但这种共享有状态组件的设计模式本身就是反模式的。
	// 它依赖于调用者（gRPC 框架）的内部实现细节来保证安全，而不是组件自身的健壮性。
	// 如果 gRPC 的模型在未来版本中发生变化，或者这个组件被用于其他并发场景，那么对 `pickerBuilder.picker` 的
	// 读写操作将产生数据竞争 (Data Race)，因为没有任何锁来保护它。
	//
	//
	// 最终结论:
	// ==================================================================================================
	// 任何需要在多次调用之间保持状态的组件 (如此处的 `pickerBuilder`，它需要保存上一次的 `picker` 来计算差值)，
	// 都绝对不能像这样在 `Builder` 创建时以单例的形式注入。
	//
	// 正确的做法是：实现一个自定义的 `balancer.Builder`，并在其 `Build` 方法内部，
	// 为每一个 `balancer.Balancer` 实例创建一个全新的、独立的 `pickerBuilder` 实例。
	// 这可以确保每个服务（每个 `ClientConn`）的状态被完全隔离，互不干扰。
	// ==================================================================================================
	builder := base.NewBalancerBuilder(BalancerName, &pickerBuilder{}, base.Config{HealthCheck: true})
	balancer.Register(builder)
	return builder
}
