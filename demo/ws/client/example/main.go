package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"sync"
	"time"

	"gitee.com/flycash/ws-gateway/demo/ws/client/internal"
	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"github.com/gobwas/ws"
)

// ClientStats 客户端统计信息
type ClientStats struct {
	mu                sync.Mutex
	totalConnections  int64
	activeConnections int64
	totalMessages     int64
	successMessages   int64
	failedMessages    int64
	startTime         time.Time
}

func (s *ClientStats) incrementConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalConnections++
	s.activeConnections++
}

func (s *ClientStats) decrementConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeConnections--
}

func (s *ClientStats) incrementMessages(success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalMessages++
	if success {
		s.successMessages++
	} else {
		s.failedMessages++
	}
}

func (s *ClientStats) getStats() (int64, int64, int64, int64, int64, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	duration := time.Since(s.startTime)
	return s.totalConnections, s.activeConnections, s.totalMessages, s.successMessages, s.failedMessages, duration
}

// WebSocketClientWrapper WebSocket客户端包装器
type WebSocketClientWrapper struct {
	client   *internal.WebSocketClient
	userID   int64
	bizID    int64
	stats    *ClientStats
	ctx      context.Context
	cancel   context.CancelFunc
	stopChan chan struct{}
}

func NewWebSocketClientWrapper(conn net.Conn, bizID, userID int64, stats *ClientStats) *WebSocketClientWrapper {
	ctx, cancel := context.WithCancel(context.Background())
	return &WebSocketClientWrapper{
		client:   internal.NewWebSocketClient(conn, bizID, userID),
		userID:   userID,
		bizID:    bizID,
		stats:    stats,
		ctx:      ctx,
		cancel:   cancel,
		stopChan: make(chan struct{}),
	}
}

func (w *WebSocketClientWrapper) Start() error {
	w.stats.incrementConnections()

	// 启动消息循环
	go func() {
		if err := w.client.Start(w.ctx); err != nil {
			log.Printf("客户端 %d 消息循环错误: %v", w.userID, err)
		}
		w.stats.decrementConnections()
	}()

	return nil
}

func (w *WebSocketClientWrapper) SendMessage(content string) error {
	err := w.client.SendUpstreamMessage(content)
	w.stats.incrementMessages(err == nil)
	return err
}

func (w *WebSocketClientWrapper) Stop() {
	w.cancel()
	w.client.Stop()
	close(w.stopChan)
}

// 全局统计信息
var stats = &ClientStats{}

func main() {
	// 解析命令行参数
	numClients := flag.Int("clients", 10, "客户端连接数量")
	messagesPerSecond := flag.Int("mps", 1, "每秒发送消息数量")
	testDuration := flag.Duration("duration", 1*time.Minute, "测试持续时间")
	connectionTimeout := flag.Duration("conn-timeout", 10*time.Second, "连接超时时间")
	responseTimeout := flag.Duration("resp-timeout", 1*time.Second, "响应超时时间")
	rampUpTime := flag.Duration("ramp-up", 10*time.Second, "连接建立阶段时间")
	messageSize := flag.Int("msg-size", 100, "消息大小(字节)")
	serverURL := flag.String("server", "ws://localhost:9002/ws", "WebSocket服务器地址")
	flag.Parse()

	// 初始化统计信息
	stats.startTime = time.Now()

	log.Printf("开始WebSocket压力测试:")
	log.Printf("  客户端数量: %d", *numClients)
	log.Printf("  每秒消息数: %d", *messagesPerSecond)
	log.Printf("  测试持续时间: %v", *testDuration)
	log.Printf("  连接超时: %v", *connectionTimeout)
	log.Printf("  响应超时: %v", *responseTimeout)
	log.Printf("  连接建立时间: %v", *rampUpTime)
	log.Printf("  消息大小: %d 字节", *messageSize)
	log.Printf("  服务器地址: %s", *serverURL)

	// 创建测试上下文
	testCtx, testCancel := context.WithTimeout(context.Background(), *testDuration)
	defer testCancel()

	// 创建客户端连接
	clients := make([]*WebSocketClientWrapper, 0, *numClients)
	var wg sync.WaitGroup

	// 连接建立阶段
	log.Printf("开始建立 %d 个客户端连接...", *numClients)
	connectionStart := time.Now()

	for i := 0; i < *numClients; i++ {
		wg.Add(1)
		go func(clientIndex int) {
			defer wg.Done()

			// 生成JWT token
			tokenGenerator := jwt.NewUserToken(jwt.UserJWTKey, "mock")
			claims := jwt.UserClaims{
				UserID: int64(10000 + clientIndex), // 确保每个客户端有不同的UserID
				BizID:  9999,
			}
			token, err := tokenGenerator.Encode(claims)
			if err != nil {
				log.Printf("客户端 %d 生成token失败: %v", clientIndex, err)
				return
			}

			// 构建WebSocket URL
			wsURL := fmt.Sprintf("%s?token=%s", *serverURL, url.QueryEscape(token))

			// 建立WebSocket连接
			connCtx, connCancel := context.WithTimeout(context.Background(), *connectionTimeout)
			defer connCancel()

			conn, _, _, err := ws.Dial(connCtx, wsURL)
			if err != nil {
				log.Printf("客户端 %d 连接失败: %v", clientIndex, err)
				return
			}

			// 创建客户端包装器
			client := NewWebSocketClientWrapper(conn, 9999, int64(10000+clientIndex), stats)
			clients = append(clients, client)

			// 启动客户端
			if err := client.Start(); err != nil {
				log.Printf("客户端 %d 启动失败: %v", clientIndex, err)
				conn.Close()
				return
			}

			log.Printf("客户端 %d 连接成功", clientIndex)
		}(i)

		// 控制连接建立速率
		if *rampUpTime > 0 {
			time.Sleep(*rampUpTime / time.Duration(*numClients))
		}
	}

	// 等待所有连接建立完成
	wg.Wait()
	connectionDuration := time.Since(connectionStart)
	log.Printf("连接建立完成，耗时: %v", connectionDuration)

	// 消息发送阶段
	log.Printf("开始发送消息...")
	messageTicker := time.NewTicker(time.Duration(1000/(*messagesPerSecond)) * time.Millisecond)
	defer messageTicker.Stop()

	// 生成测试消息
	testMessage := generateTestMessage(*messageSize)

	// 启动消息发送goroutine
	go func() {
		clientIndex := 0
		for {
			select {
			case <-testCtx.Done():
				return
			case <-messageTicker.C:
				if len(clients) == 0 {
					continue
				}

				// 轮询发送消息给不同的客户端
				client := clients[clientIndex%len(clients)]
				clientIndex++

				go func(c *WebSocketClientWrapper) {
					// 使用响应超时
					msgCtx, msgCancel := context.WithTimeout(context.Background(), *responseTimeout)
					defer msgCancel()

					// 在goroutine中发送消息
					go func() {
						if err := c.SendMessage(testMessage); err != nil {
							log.Printf("发送消息失败: %v", err)
						}
					}()

					// 等待超时或完成
					select {
					case <-msgCtx.Done():
						log.Printf("消息发送超时")
					case <-time.After(100 * time.Millisecond): // 给一点时间让消息发送
					}
				}(client)
			}
		}
	}()

	// 定期打印统计信息
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-testCtx.Done():
				return
			case <-ticker.C:
				printStats()
			}
		}
	}()

	// 等待测试结束
	<-testCtx.Done()
	log.Printf("测试结束")

	// 停止所有客户端
	log.Printf("正在停止所有客户端...")
	for _, client := range clients {
		client.Stop()
	}

	// 打印最终统计信息
	printStats()
	log.Printf("WebSocket压力测试完成")
}

// generateTestMessage 生成指定大小的测试消息
func generateTestMessage(size int) string {
	if size <= 0 {
		return "test"
	}

	// 生成指定大小的消息
	message := make([]byte, size)
	for i := range message {
		message[i] = byte('a' + (i % 26))
	}
	return string(message)
}

// printStats 打印统计信息
func printStats() {
	totalConn, activeConn, totalMsg, successMsg, failedMsg, duration := stats.getStats()

	log.Printf("=== 统计信息 ===")
	log.Printf("总连接数: %d", totalConn)
	log.Printf("活跃连接数: %d", activeConn)
	log.Printf("总消息数: %d", totalMsg)
	log.Printf("成功消息数: %d", successMsg)
	log.Printf("失败消息数: %d", failedMsg)
	log.Printf("运行时间: %v", duration)

	if duration > 0 {
		msgPerSec := float64(totalMsg) / duration.Seconds()
		log.Printf("消息速率: %.2f msg/s", msgPerSec)
	}

	if totalMsg > 0 {
		successRate := float64(successMsg) / float64(totalMsg) * 100
		log.Printf("成功率: %.2f%%", successRate)
	}
	log.Printf("================")
}
