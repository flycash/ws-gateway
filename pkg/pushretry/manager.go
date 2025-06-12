package pushretry

import (
	"sync/atomic"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/ecodeclub/ekit/syncx"
	"github.com/gotomicro/ego/core/elog"
)

// PushFunc 定义重新发送消息的函数类型
type PushFunc func(lk gateway.Link, msg *apiv1.Message) error

// Manager 下行消息重传管理器
type Manager struct {
	tasks         *syncx.Map[string, *Task] // key -> 重传任务
	totalTasks    atomic.Int64              // 任务总数
	retryInterval time.Duration             // 重传间隔
	maxRetries    int                       // 最大重试次数
	pushFunc      PushFunc                  // 重新发送消息的函数
	logger        *elog.Component           // 日志组件
	closed        atomic.Bool               // 是否已关闭
}

// NewManager 创建重传管理器
func NewManager(retryInterval time.Duration, maxRetries int, pushFunc PushFunc) *Manager {
	return &Manager{
		tasks:         &syncx.Map[string, *Task]{},
		retryInterval: retryInterval,
		maxRetries:    maxRetries,
		pushFunc:      pushFunc,
		logger:        elog.EgoLogger.With(elog.FieldComponent("pushretry.Manager")),
	}
}

// Start 启动重传任务
func (m *Manager) Start(key string, link gateway.Link, message *apiv1.Message) {
	if m.closed.Load() {
		return
	}

	task := &Task{
		key:     key,
		link:    link,
		message: message,
		manager: m,
	}
	// 如果已存在相同key的任务就忽略
	if _, loaded := m.tasks.LoadOrStore(key, task); loaded {
		return
	}

	// 启动任务的定时器
	task.timerPtr.Store(time.AfterFunc(m.retryInterval, task.retry))
	m.totalTasks.Add(1)

	m.logger.Info("启动重传任务",
		elog.String("key", key),
		elog.String("linkID", link.ID()),
		elog.Duration("retryInterval", m.retryInterval))
}

// Stop 停止指定key的重传任务
func (m *Manager) Stop(key string) {
	task, ok := m.tasks.Load(key)
	if !ok {
		return
	}
	m.stopAndDeleteTask(key)
	m.logger.Info("停止重传任务",
		elog.String("key", key),
		elog.Any("retryCount", task.retryCount.Load()))
}

func (m *Manager) stopAndDeleteTask(key string) bool {
	if task, ok := m.tasks.LoadAndDelete(key); ok {
		task.stop()
		m.totalTasks.Add(-1)
		return true
	}
	return false
}

// StopByLinkID 停止指定连接的所有重传任务
func (m *Manager) StopByLinkID(linkID string) {
	var taskCount int
	m.tasks.Range(func(key string, task *Task) bool {
		if task.link.ID() == linkID {
			if m.stopAndDeleteTask(key) {
				taskCount++
			}
		}
		return true
	})
	if taskCount > 0 {
		m.logger.Info("清理连接的重传任务",
			elog.String("linkID", linkID),
			elog.Int("taskCount", taskCount))
	}
}

// GetStats 获取统计信息（用于测试和监控）
func (m *Manager) GetStats() (activeCount int64) {
	return m.totalTasks.Load()
}

// Close 关闭管理器，清理所有重传任务
func (m *Manager) Close() {
	m.closed.Store(true)
	m.tasks.Range(func(key string, _ *Task) bool {
		m.stopAndDeleteTask(key)
		return true // 确保继续遍历
	})
	m.logger.Info("重传管理器已关闭")
}

// Task 单个重传任务
type Task struct {
	key        string                     // 消息唯一标识
	link       gateway.Link               // WebSocket连接
	message    *apiv1.Message             // 待重传的消息
	timerPtr   atomic.Pointer[time.Timer] // 重传定时器
	retryCount atomic.Int64               // 当前重试次数
	manager    *Manager                   // 所属管理器的引用
}

// retry 定时器超时处理
func (t *Task) retry() {
	// 检查任务是否还存在（可能已被停止）
	if _, exists := t.manager.tasks.Load(t.key); !exists {
		return
	}

	t.retryCount.Add(1)

	// 检查是否达到最大重试次数
	if t.retryCount.Load() >= int64(t.manager.maxRetries) {
		t.manager.logger.Warn("重传任务达到最大重试次数，停止重传",
			elog.String("key", t.key),
			elog.String("linkID", t.link.ID()),
			elog.Any("retryCount", t.retryCount.Load()))
		t.manager.stopAndDeleteTask(t.key)
		return
	}

	// 重新发送消息
	err := t.manager.pushFunc(t.link, t.message)
	if err != nil {
		t.manager.logger.Error("重传消息失败",
			elog.String("key", t.key),
			elog.String("linkID", t.link.ID()),
			elog.Any("retryCount", t.retryCount.Load()),
			elog.FieldErr(err))
		// 如果发送失败（比如连接已断开），停止重传
		t.manager.stopAndDeleteTask(t.key)
		return
	}

	// 重传成功，更新活跃时间
	t.link.UpdateActiveTime()

	t.manager.logger.Info("重传消息成功",
		elog.String("key", t.key),
		elog.String("linkID", t.link.ID()),
		elog.Any("retryCount", t.retryCount.Load()))

	// 重新设置定时器
	t.timerPtr.Store(time.AfterFunc(t.manager.retryInterval, t.retry))
}

// stop 停止任务
func (t *Task) stop() {
	if timer := t.timerPtr.Load(); timer != nil {
		timer.Stop()
	}
}
