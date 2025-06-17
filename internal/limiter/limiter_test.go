//go:build unit

package limiter_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"gitee.com/flycash/ws-gateway/internal/limiter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stretchr/testify/suite"
)

// TestTokenLimiterSuite 是测试套件的入口点。
func TestTokenLimiterSuite(t *testing.T) {
	suite.Run(t, new(TokenLimiterSuite))
}

// TokenLimiterSuite 是一个遵循 testify/suite 规范的测试套件。
type TokenLimiterSuite struct {
	suite.Suite
}

// TestNewTokenLimiter_Validation 测试构造函数的参数校验逻辑。
func (s *TokenLimiterSuite) TestNewTokenLimiter_Validation() {
	t := s.T()
	testCases := []struct {
		name      string
		cfg       limiter.TokenLimiterConfig
		expectErr bool
	}{
		{
			name:      "有效配置",
			cfg:       limiter.TokenLimiterConfig{InitialCapacity: 10, MaxCapacity: 100, IncreaseStep: 5, IncreaseInterval: time.Second},
			expectErr: false,
		},
		{
			name:      "无效: MaxCapacity为0",
			cfg:       limiter.TokenLimiterConfig{InitialCapacity: 10, MaxCapacity: 0, IncreaseStep: 5, IncreaseInterval: time.Second},
			expectErr: true,
		},
		{
			name:      "无效: InitialCapacity小于0",
			cfg:       limiter.TokenLimiterConfig{InitialCapacity: -10, MaxCapacity: 10, IncreaseStep: 5, IncreaseInterval: time.Second},
			expectErr: true,
		},
		{
			name:      "无效: InitialCapacity大于MaxCapacity",
			cfg:       limiter.TokenLimiterConfig{InitialCapacity: 101, MaxCapacity: 100, IncreaseStep: 5, IncreaseInterval: time.Second},
			expectErr: true,
		},
		{
			name:      "无效: IncreaseStep小于0",
			cfg:       limiter.TokenLimiterConfig{InitialCapacity: 10, MaxCapacity: 10, IncreaseStep: -5, IncreaseInterval: time.Second},
			expectErr: true,
		},
		{
			name:      "无效: IncreaseStep等于0",
			cfg:       limiter.TokenLimiterConfig{InitialCapacity: 10, MaxCapacity: 10, IncreaseStep: 0, IncreaseInterval: time.Second},
			expectErr: true,
		},
		{
			name:      "无效: IncreaseInterval小于0",
			cfg:       limiter.TokenLimiterConfig{InitialCapacity: 10, MaxCapacity: 10, IncreaseStep: 1, IncreaseInterval: -2},
			expectErr: true,
		},
		{
			name:      "无效: IncreaseInterval等于0",
			cfg:       limiter.TokenLimiterConfig{InitialCapacity: 10, MaxCapacity: 10, IncreaseStep: 1, IncreaseInterval: 0},
			expectErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := limiter.NewTokenLimiter(tc.cfg)
			if tc.expectErr {
				assert.Error(t, err, "预期会出现一个错误")
			} else {
				assert.NoError(t, err, "预期不会出现错误")
				// 关闭以清理资源
				assert.NoError(t, l.Close())
			}
		})
	}
}

// TestAcquireAndRelease 测试基本的令牌获取和释放功能。
func (s *TokenLimiterSuite) TestAcquireAndRelease() {
	t := s.T()
	cfg := limiter.TokenLimiterConfig{InitialCapacity: 2, MaxCapacity: 10, IncreaseStep: 2, IncreaseInterval: time.Millisecond}
	l, err := limiter.NewTokenLimiter(cfg)
	require.NoError(t, err)
	defer l.Close()

	assert.True(t, l.Acquire(), "第1次获取应该成功")
	assert.True(t, l.Acquire(), "第2次获取应该成功")
	assert.False(t, l.Acquire(), "第3次获取应该失败")
	assert.True(t, l.Release())
	assert.True(t, l.Acquire(), "释放后应该能再次获取成功")
}

// TestRampUp_Full 测试容量增长的全过程。
func (s *TokenLimiterSuite) TestRampUp_Full() {
	t := s.T()
	cfg := limiter.TokenLimiterConfig{InitialCapacity: 2, MaxCapacity: 10, IncreaseStep: 3, IncreaseInterval: 20 * time.Millisecond}
	l, err := limiter.NewTokenLimiter(cfg)
	require.NoError(t, err)
	defer l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go l.StartRampUp(ctx)

	totalSteps := (cfg.MaxCapacity-cfg.InitialCapacity)/cfg.IncreaseStep + 1
	timeout := time.Duration(totalSteps+1) * cfg.IncreaseInterval

	assert.Eventually(t, func() bool {
		// ✨ 使用新增的 CurrentCapacity() 公共方法来检查状态
		return l.CurrentCapacity() == cfg.MaxCapacity
	}, timeout, 5*time.Millisecond, fmt.Sprintf("容量应在规定时间内增长到最大值: CurrentCapacity = %d, MaxCapacity = %d", l.CurrentCapacity(), cfg.MaxCapacity))

	// 确认 RampUp 的 goroutine 已经因达到最大值而退出
	time.Sleep(cfg.IncreaseInterval)

	// 此时应该可以获取所有 MaxCapacity 个令牌
	for i := int64(0); i < cfg.MaxCapacity; i++ {
		assert.True(t, l.Acquire(), "在容量达到最大后，第 %d 次获取应该成功", i+1)
	}
	assert.False(t, l.Acquire(), "所有令牌都获取后，再次获取应该失败")
}

// TestRampUp_CancelWithExternalContext 测试通过外部 context 来终止容量增长。
func (s *TokenLimiterSuite) TestRampUp_CancelWithExternalContext() {
	t := s.T()
	cfg := limiter.TokenLimiterConfig{InitialCapacity: 2, MaxCapacity: 10, IncreaseStep: 2, IncreaseInterval: 20 * time.Millisecond}
	l, err := limiter.NewTokenLimiter(cfg)
	require.NoError(t, err)
	defer l.Close()

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		l.StartRampUp(ctx)
	}()

	time.Sleep(cfg.IncreaseInterval*2 + 5*time.Millisecond) // 等待几轮增长
	cancel()                                                // 取消 context
	wg.Wait()                                               // 等待 goroutine 退出

	capacityAfterCancel := l.CurrentCapacity()
	assert.Greater(t, capacityAfterCancel, cfg.InitialCapacity, "容量应该已经增长了一些")
	assert.Less(t, capacityAfterCancel, cfg.MaxCapacity, "容量不应该达到最大值")

	time.Sleep(cfg.IncreaseInterval * 2) // 再等一会
	assert.Equal(t, capacityAfterCancel, l.CurrentCapacity(), "取消后，容量不应再发生变化")
}

// TestRampUp_CancelWithClose 测试通过调用 Close() 方法来终止容量增长。
func (s *TokenLimiterSuite) TestRampUp_CancelWithClose() {
	t := s.T()
	cfg := limiter.TokenLimiterConfig{InitialCapacity: 2, MaxCapacity: 10, IncreaseStep: 2, IncreaseInterval: 20 * time.Millisecond}
	l, err := limiter.NewTokenLimiter(cfg)
	require.NoError(t, err)
	// defer l.Close() // 我们将手动调用 Close

	ctx := context.Background() // 使用一个不会被取消的 context

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		l.StartRampUp(ctx)
	}()

	time.Sleep(cfg.IncreaseInterval*2 + 5*time.Millisecond)
	err = l.Close() // 调用 Close 来停止
	assert.NoError(t, err)
	wg.Wait()

	capacityAfterClose := l.CurrentCapacity()
	assert.Greater(t, capacityAfterClose, cfg.InitialCapacity)
	assert.Less(t, capacityAfterClose, cfg.MaxCapacity)

	time.Sleep(cfg.IncreaseInterval * 2)
	assert.Equal(t, capacityAfterClose, l.CurrentCapacity())
}

// TestReleaseToFullLimiter 测试向一个已满的令牌桶释放令牌时不会阻塞。
func (s *TokenLimiterSuite) TestReleaseToFullLimiter() {
	t := s.T()
	cfg := limiter.TokenLimiterConfig{InitialCapacity: 2, MaxCapacity: 2, IncreaseStep: 1, IncreaseInterval: time.Minute}
	l, err := limiter.NewTokenLimiter(cfg)
	require.NoError(t, err)
	defer l.Close()

	assert.True(t, l.Acquire())
	assert.True(t, l.Acquire()) // 拿光所有令牌
	assert.True(t, l.Release()) // 还回去一个
	assert.True(t, l.Release()) // 再还回去一个，现在满了

	done := make(chan struct{})
	go func() {
		assert.False(t, l.Release()) // 尝试向满的桶释放，如果阻塞，测试将超时
		close(done)
	}()

	select {
	case <-done:
		// 测试通过
	case <-time.After(100 * time.Millisecond):
		assert.Fail(t, "Release() 方法在令牌桶已满时不应阻塞")
	}
}

// TestClose_Idempotency 测试 Close 方法的幂等性。
func (s *TokenLimiterSuite) TestClose_Idempotency() {
	t := s.T()
	cfg := limiter.TokenLimiterConfig{InitialCapacity: 2, MaxCapacity: 10, IncreaseStep: 2, IncreaseInterval: time.Millisecond}
	l, err := limiter.NewTokenLimiter(cfg)
	require.NoError(t, err)

	assert.NoError(t, l.Close())
	assert.NoError(t, l.Close())
	assert.NoError(t, l.Close())
}
