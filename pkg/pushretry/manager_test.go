//go:build unit

package pushretry_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/pkg/pushretry"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type ManagerTestSuite struct {
	suite.Suite
}

func TestManagerTestSuite(t *testing.T) {
	suite.Run(t, &ManagerTestSuite{})
}

func (s *ManagerTestSuite) TestNewManager() {
	pushFunc := func(lk gateway.Link, msg *apiv1.Message) error {
		return nil
	}

	manager := pushretry.NewManager(time.Minute, 3, pushFunc)
	s.NotNil(manager)
	s.Equal(0, manager.GetStats())
}

func (s *ManagerTestSuite) TestStart() {
	t := s.T()

	callCount := 0
	var mu sync.Mutex
	pushFunc := func(lk gateway.Link, msg *apiv1.Message) error {
		mu.Lock()
		callCount++
		mu.Unlock()
		return nil
	}

	manager := pushretry.NewManager(50*time.Millisecond, 3, pushFunc)
	defer manager.Close()

	// 创建测试连接
	lk := s.createTestLink("test-1")
	defer lk.Close()

	// 创建测试消息
	msg := &apiv1.Message{
		Cmd:   apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
		BizId: 1,
		Key:   "test-key-1",
		Body:  []byte("test body"),
	}

	// 启动重传任务
	manager.Start("test-key-1", lk, msg)
	s.Equal(1, manager.GetStats())

	// 等待重传触发
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	assert.GreaterOrEqual(t, callCount, 1, "应该触发重传")
	mu.Unlock()
}

func (s *ManagerTestSuite) TestStop() {
	callCount := 0
	var mu sync.Mutex
	pushFunc := func(lk gateway.Link, msg *apiv1.Message) error {
		mu.Lock()
		callCount++
		mu.Unlock()
		return nil
	}

	manager := pushretry.NewManager(50*time.Millisecond, 3, pushFunc)
	defer manager.Close()

	lk := s.createTestLink("test-2")
	defer lk.Close()

	msg := &apiv1.Message{
		Cmd:   apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
		BizId: 1,
		Key:   "test-key-2",
		Body:  []byte("test body"),
	}

	// 启动重传任务
	manager.Start("test-key-2", lk, msg)
	s.Equal(1, manager.GetStats())

	// 立即停止
	manager.Stop("test-key-2")
	s.Equal(0, manager.GetStats())

	// 等待一段时间，确认没有重传
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	finalCallCount := callCount
	mu.Unlock()
	s.Equal(0, finalCallCount, "停止后不应该有重传")
}

func (s *ManagerTestSuite) TestStopByLinkID() {
	callCount := 0
	var mu sync.Mutex
	pushFunc := func(lk gateway.Link, msg *apiv1.Message) error {
		mu.Lock()
		callCount++
		mu.Unlock()
		return nil
	}

	manager := pushretry.NewManager(50*time.Millisecond, 3, pushFunc)
	defer manager.Close()

	lk1 := s.createTestLink("test-3-1")
	defer lk1.Close()
	lk2 := s.createTestLink("test-3-2")
	defer lk2.Close()

	// 为两个连接创建重传任务
	msg1 := &apiv1.Message{
		Cmd:   apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
		BizId: 1,
		Key:   "test-key-3-1",
		Body:  []byte("test body 1"),
	}
	msg2 := &apiv1.Message{
		Cmd:   apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
		BizId: 1,
		Key:   "test-key-3-2",
		Body:  []byte("test body 2"),
	}

	manager.Start("test-key-3-1", lk1, msg1)
	manager.Start("test-key-3-2", lk2, msg2)
	s.Equal(2, manager.GetStats())

	// 停止lk1的所有任务
	manager.StopByLinkID(lk1.ID())
	s.Equal(1, manager.GetStats())

	// 停止lk2的所有任务
	manager.StopByLinkID(lk2.ID())
	s.Equal(0, manager.GetStats())
}

func (s *ManagerTestSuite) TestMaxRetries() {
	t := s.T()

	callCount := 0
	var mu sync.Mutex
	pushFunc := func(lk gateway.Link, msg *apiv1.Message) error {
		mu.Lock()
		callCount++
		mu.Unlock()
		return nil
	}

	// 设置最大重试次数为2
	manager := pushretry.NewManager(50*time.Millisecond, 2, pushFunc)
	defer manager.Close()

	lk := s.createTestLink("test-4")
	defer lk.Close()

	msg := &apiv1.Message{
		Cmd:   apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
		BizId: 1,
		Key:   "test-key-4",
		Body:  []byte("test body"),
	}

	manager.Start("test-key-4", lk, msg)
	s.Equal(1, manager.GetStats())

	// 等待足够长的时间让重传达到最大次数
	time.Sleep(300 * time.Millisecond)

	// 任务应该被自动清理
	s.Equal(0, manager.GetStats())

	// 验证重传次数不超过最大值
	mu.Lock()
	assert.LessOrEqual(t, callCount, 2, "重传次数不应超过最大值")
	mu.Unlock()
}

func (s *ManagerTestSuite) TestPushFuncError() {
	pushFunc := func(lk gateway.Link, msg *apiv1.Message) error {
		return errors.New("push failed")
	}

	manager := pushretry.NewManager(50*time.Millisecond, 3, pushFunc)
	defer manager.Close()

	lk := s.createTestLink("test-5")
	defer lk.Close()

	msg := &apiv1.Message{
		Cmd:   apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
		BizId: 1,
		Key:   "test-key-5",
		Body:  []byte("test body"),
	}

	manager.Start("test-key-5", lk, msg)
	s.Equal(1, manager.GetStats())

	// 等待推送失败导致任务被清理
	time.Sleep(100 * time.Millisecond)
	s.Equal(0, manager.GetStats())
}

func (s *ManagerTestSuite) TestClose() {
	callCount := 0
	var mu sync.Mutex
	pushFunc := func(lk gateway.Link, msg *apiv1.Message) error {
		mu.Lock()
		callCount++
		mu.Unlock()
		return nil
	}

	manager := pushretry.NewManager(50*time.Millisecond, 3, pushFunc)

	lk := s.createTestLink("test-6")
	defer lk.Close()

	msg := &apiv1.Message{
		Cmd:   apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
		BizId: 1,
		Key:   "test-key-6",
		Body:  []byte("test body"),
	}

	// 启动多个重传任务
	manager.Start("test-key-6-1", lk, msg)
	manager.Start("test-key-6-2", lk, msg)
	s.Equal(2, manager.GetStats())

	// 关闭管理器
	manager.Close()
	s.Equal(0, manager.GetStats())

	// 关闭后启动新任务应该被忽略
	manager.Start("test-key-6-3", lk, msg)
	s.Equal(0, manager.GetStats())
}

func (s *ManagerTestSuite) TestDuplicateKey() {
	callCount := 0
	var mu sync.Mutex
	pushFunc := func(lk gateway.Link, msg *apiv1.Message) error {
		mu.Lock()
		callCount++
		mu.Unlock()
		return nil
	}

	manager := pushretry.NewManager(50*time.Millisecond, 3, pushFunc)
	defer manager.Close()

	lk := s.createTestLink("test-7")
	defer lk.Close()

	msg1 := &apiv1.Message{
		Cmd:   apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
		BizId: 1,
		Key:   "duplicate-key",
		Body:  []byte("test body 1"),
	}
	msg2 := &apiv1.Message{
		Cmd:   apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
		BizId: 1,
		Key:   "duplicate-key",
		Body:  []byte("test body 2"),
	}

	// 启动第一个任务
	manager.Start("duplicate-key", lk, msg1)
	s.Equal(1, manager.GetStats())

	// 启动相同key的第二个任务，应该替换第一个
	manager.Start("duplicate-key", lk, msg2)
	s.Equal(1, manager.GetStats())
}

func (s *ManagerTestSuite) TestConcurrentOperations() {
	pushFunc := func(lk gateway.Link, msg *apiv1.Message) error {
		return nil
	}

	manager := pushretry.NewManager(time.Second, 3, pushFunc)
	defer manager.Close()

	lk := s.createTestLink("test-8")
	defer lk.Close()

	// 并发启动多个任务
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("concurrent-key-%d", id)
			msg := &apiv1.Message{
				Cmd:   apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
				BizId: 1,
				Key:   key,
				Body:  []byte("test body"),
			}
			manager.Start(key, lk, msg)
		}(i)
	}
	wg.Wait()

	s.Equal(10, manager.GetStats())

	// 并发停止任务
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("concurrent-key-%d", id)
			manager.Stop(key)
		}(i)
	}
	wg.Wait()

	s.Equal(0, manager.GetStats())
}

func (s *ManagerTestSuite) createTestLink(linkID string) gateway.Link {
	server, _ := net.Pipe()
	return link.New(context.Background(), linkID, session.Session{BizID: 1, UserID: 123}, server)
}
