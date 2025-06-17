package limiter

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gotomicro/ego/core/elog"
)

// TokenLimiterConfig 结构体用于配置 TokenLimiter。
type TokenLimiterConfig struct {
	InitialCapacity  int64         `yaml:"initialCapacity"`  // 启动时的初始容量
	MaxCapacity      int64         `yaml:"maxCapacity"`      // 最终的稳定容量
	IncreaseStep     int64         `yaml:"increaseStep"`     // 每次增加的容量步长
	IncreaseInterval time.Duration `yaml:"increaseInterval"` // 每次增加容量的时间间隔
}

// TokenLimiter 通过令牌桶算法管理并发数，并支持容量的动态、逐步增长。
// 它是一个独立、可控生命周期的组件，并且是并发安全的。
type TokenLimiter struct {
	config          TokenLimiterConfig // 限流器配置
	currentCapacity atomic.Int64       // 当前的实时容量
	tokens          chan struct{}      // 用作令牌桶

	// 组件内部的 context，用于通过 Close 方法从外部控制其生命周期。
	ctx    context.Context
	cancel context.CancelFunc

	logger *elog.Component
}

// NewTokenLimiter 使用指定的配置创建一个新的 TokenLimiter 实例。
// 它会对配置进行严格校验，如果配置无效，将返回错误。
func NewTokenLimiter(cfg TokenLimiterConfig) (*TokenLimiter, error) {
	// 1. 严格校验参数，提供更具体的错误信息
	if cfg.MaxCapacity <= 0 {
		return nil, errors.New("配置错误: MaxCapacity 必须为正数")
	}
	if cfg.InitialCapacity < 0 {
		return nil, errors.New("配置错误: InitialCapacity 不能为负数")
	}
	if cfg.InitialCapacity > cfg.MaxCapacity {
		return nil, fmt.Errorf("配置错误: InitialCapacity (%d) 不能大于 MaxCapacity (%d)", cfg.InitialCapacity, cfg.MaxCapacity)
	}
	if cfg.IncreaseStep <= 0 {
		return nil, errors.New("配置错误: IncreaseStep 必须为正数")
	}
	if cfg.IncreaseInterval <= 0 {
		return nil, errors.New("配置错误: IncreaseInterval 必须为正数")
	}

	// 2. 创建实例
	ctx, cancel := context.WithCancel(context.Background())
	l := &TokenLimiter{
		config: cfg,
		tokens: make(chan struct{}, cfg.MaxCapacity),
		ctx:    ctx,
		cancel: cancel,
		logger: elog.DefaultLogger.With(elog.FieldComponent("TokenLimiter")),
	}

	// 3. 填充初始令牌
	for i := int64(0); i < cfg.InitialCapacity; i++ {
		l.tokens <- struct{}{}
	}
	l.currentCapacity.Store(cfg.InitialCapacity)

	l.logger.Info("令牌限流器初始化成功",
		elog.Int64("currentCapacity", l.currentCapacity.Load()),
		elog.Int64("maxCapacity", l.config.MaxCapacity),
	)
	return l, nil
}

// StartRampUp 启动一个后台 goroutine，该 goroutine 会逐步增加令牌桶的容量。
// 调用者需要负责在独立的 goroutine 中运行此方法。
//
// 此方法提供了两种停止机制：
// 1. 外部传入的 ctx：当这个 ctx 被取消时（例如，与单个请求或临时任务绑定），goroutine 会退出。
// 2. 内部的 ctx：当调用 TokenLimiter 的 Close 方法时，内部 ctx 会被取消，goroutine 也会退出。
func (t *TokenLimiter) StartRampUp(ctx context.Context) {
	ticker := time.NewTicker(t.config.IncreaseInterval)
	defer ticker.Stop()

	t.logger.Info("启动容量增长（Ramp-Up）过程")

	for {
		select {
		case <-ctx.Done(): // 监听来自方法参数的取消信号
			t.logger.Info("外部上下文取消，停止容量增长过程", elog.FieldErr(ctx.Err()))
			return
		case <-t.ctx.Done(): // 监听来自组件内部的取消信号 (由Close触发)
			t.logger.Info("内部上下文取消，停止容量增长过程")
			return
		case <-ticker.C:
			current := t.currentCapacity.Load()
			if current >= t.config.MaxCapacity {
				t.logger.Info("容量已达到最大值，停止增长")
				// 容量已满，这个 goroutine 的使命已经完成，可以安全退出了。
				return
			}

			// 计算本次增长后的新容量
			newCapacity := current + t.config.IncreaseStep
			if newCapacity > t.config.MaxCapacity {
				newCapacity = t.config.MaxCapacity
			}

			// 向令牌桶中添加增量令牌
			addedTokens := newCapacity - current
			for i := int64(0); i < addedTokens; i++ {
				t.tokens <- struct{}{}
			}
			t.currentCapacity.Store(newCapacity)

			t.logger.Info("令牌限流器容量增加",
				elog.Int64("from", current),
				elog.Int64("to", newCapacity),
				elog.Int64("step", t.config.IncreaseStep),
			)
		}
	}
}

// Acquire 尝试获取一个令牌。
// 这是一个非阻塞操作。如果成功获取到令牌，返回 true；
// 如果当前没有可用令牌，立即返回 false。
func (t *TokenLimiter) Acquire() bool {
	select {
	case <-t.tokens:
		return true // 成功获取令牌
	default:
		return false // 令牌桶已空
	}
}

// Release 归还一个令牌。非阻塞。
func (t *TokenLimiter) Release() bool {
	select {
	case t.tokens <- struct{}{}:
		return true
	default:
		// 这种情况理论上不应该发生，除非 Release 的调用次数超过了 Acquire。
		// 这通常意味着代码中存在逻辑错误。
		t.logger.Warn("尝试归还令牌失败，令牌桶已满")
		return false
	}
}

// Close 会取消组件内部的 context，从而通知所有由该 limiter 启动的后台 goroutine 停止。
// 这是一个优雅关闭的必要部分，应该在服务关闭时被调用。
// 这个方法是幂等的，可以安全地多次调用。
func (t *TokenLimiter) Close() error {
	t.cancel()
	t.logger.Info("TokenLimiter 已关闭")
	return nil
}

// CurrentCapacity 返回限流器当前的实时容量。
// 这个方法是新增的，用于支持包外测试，让测试代码可以检查内部状态。
func (t *TokenLimiter) CurrentCapacity() int64 {
	return t.currentCapacity.Load()
}
