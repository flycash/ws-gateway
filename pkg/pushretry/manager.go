package pushretry

import (
	"sync"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/gotomicro/ego/core/elog"
)

// PushFunc 定义重新发送消息的函数类型
type PushFunc func(lk gateway.Link, msg *apiv1.Message) error

// Manager 下行消息重传管理器
type Manager struct {
	tasks         map[string]*Task // key -> 重传任务
	mutex         sync.RWMutex     // 保证并发安全
	retryInterval time.Duration    // 重传间隔
	maxRetries    int              // 最大重试次数
	pushFunc      PushFunc         // 重新发送消息的函数
	logger        *elog.Component  // 日志组件
	closed        bool             // 是否已关闭
}

// Task 单个重传任务
type Task struct {
	key        string         // 消息唯一标识
	link       gateway.Link   // WebSocket连接
	message    *apiv1.Message // 待重传的消息
	timer      *time.Timer    // 重传定时器
	retryCount int            // 当前重试次数
	createdAt  time.Time      // 创建时间
	manager    *Manager       // 所属管理器的引用
}

// NewManager 创建重传管理器
func NewManager(retryInterval time.Duration, maxRetries int, pushFunc PushFunc) *Manager {
	return &Manager{
		tasks:         make(map[string]*Task),
		retryInterval: retryInterval,
		maxRetries:    maxRetries,
		pushFunc:      pushFunc,
		logger:        elog.EgoLogger.With(elog.FieldComponent("pushretry.Manager")),
		closed:        false,
	}
}

// Start 启动重传任务
func (m *Manager) Start(key string, link gateway.Link, message *apiv1.Message) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.closed {
		return
	}

	// 如果已存在相同key的任务，先停止旧任务
	if existingTask, exists := m.tasks[key]; exists {
		existingTask.stop()
	}

	task := &Task{
		key:        key,
		link:       link,
		message:    message,
		retryCount: 0,
		createdAt:  time.Now(),
		manager:    m,
	}

	// 启动定时器
	task.timer = time.AfterFunc(m.retryInterval, task.retry)
	m.tasks[key] = task

	m.logger.Info("启动重传任务",
		elog.String("key", key),
		elog.String("linkID", link.ID()),
		elog.Duration("retryInterval", m.retryInterval))
}

// Stop 停止指定key的重传任务
func (m *Manager) Stop(key string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if task, exists := m.tasks[key]; exists {
		task.stop()
		delete(m.tasks, key)

		m.logger.Info("停止重传任务",
			elog.String("key", key),
			elog.Int("retryCount", task.retryCount))
	}
}

// StopByLinkID 停止指定连接的所有重传任务
func (m *Manager) StopByLinkID(linkID string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	var keysToDelete []string
	for key, task := range m.tasks {
		if task.link.ID() == linkID {
			task.stop()
			keysToDelete = append(keysToDelete, key)
		}
	}

	for _, key := range keysToDelete {
		delete(m.tasks, key)
	}

	if len(keysToDelete) > 0 {
		m.logger.Info("清理连接的重传任务",
			elog.String("linkID", linkID),
			elog.Int("taskCount", len(keysToDelete)))
	}
}

// Close 关闭管理器，清理所有重传任务
func (m *Manager) Close() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.closed = true
	for key, task := range m.tasks {
		task.stop()
		delete(m.tasks, key)
	}

	m.logger.Info("重传管理器已关闭")
}

// GetStats 获取统计信息（用于测试和监控）
func (m *Manager) GetStats() (activeCount int) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	return len(m.tasks)
}

// retry 定时器超时处理
func (t *Task) retry() {
	t.manager.mutex.Lock()
	defer t.manager.mutex.Unlock()

	// 检查任务是否还存在（可能已被停止）
	if _, exists := t.manager.tasks[t.key]; !exists {
		return
	}

	t.retryCount++

	// 检查是否达到最大重试次数
	if t.retryCount >= t.manager.maxRetries {
		t.manager.logger.Warn("重传任务达到最大重试次数，停止重传",
			elog.String("key", t.key),
			elog.String("linkID", t.link.ID()),
			elog.Int("retryCount", t.retryCount))

		delete(t.manager.tasks, t.key)
		return
	}

	// 重新发送消息
	err := t.manager.pushFunc(t.link, t.message)
	if err != nil {
		t.manager.logger.Error("重传消息失败",
			elog.String("key", t.key),
			elog.String("linkID", t.link.ID()),
			elog.Int("retryCount", t.retryCount),
			elog.FieldErr(err))

		// 如果发送失败（比如连接已断开），停止重传
		delete(t.manager.tasks, t.key)
		return
	}

	t.manager.logger.Info("重传消息成功",
		elog.String("key", t.key),
		elog.String("linkID", t.link.ID()),
		elog.Int("retryCount", t.retryCount))

	// 重新设置定时器
	t.timer = time.AfterFunc(t.manager.retryInterval, t.retry)
}

// stop 停止任务
func (t *Task) stop() {
	if t.timer != nil {
		t.timer.Stop()
	}
}
