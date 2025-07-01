package internal

import (
	"sync"
	"time"
)

// ClientStats 客户端统计信息
type ClientStats struct {
	mu                sync.Mutex
	totalConnections  int64
	activeConnections int64
	totalMessages     int64
	successMessages   int64
	failedMessages    int64
	StartTime         time.Time
}

func (s *ClientStats) IncrementConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalConnections++
	s.activeConnections++
}

func (s *ClientStats) DecrementConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeConnections--
}

func (s *ClientStats) IncrementMessages(success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalMessages++
	if success {
		s.successMessages++
	} else {
		s.failedMessages++
	}
}

func (s *ClientStats) GetStats() (int64, int64, int64, int64, int64, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	duration := time.Since(s.StartTime)
	return s.totalConnections, s.activeConnections, s.totalMessages, s.successMessages, s.failedMessages, duration
}
