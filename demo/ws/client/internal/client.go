package internal

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"gitee.com/flycash/ws-gateway/pkg/encrypt"
	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	sendTimeout = 3 * time.Second
)

// WebSocketClient WebSocket客户端，表示一个连接的生命周期
type WebSocketClient struct {
	wsURL          string
	conn           net.Conn
	tokenGenerator *jwt.UserToken
	codec          codec.Codec
	encryptor      encrypt.Encryptor
	bizID          int64
	userID         int64
	stats          *ClientStats
	wg             sync.WaitGroup
	once           sync.Once
	stopChan       chan struct{}
	compressed     bool
	connMutex      sync.RWMutex // 保护连接状态的互斥锁
}

// NewWebSocketClient 创建新的WebSocket客户端
// wsURL: WebSocket服务器URL
// bizID: 业务ID
// userID: 用户ID
// stats: 统计实现（依赖注入）
// compressed: 是否启用压缩
func NewWebSocketClient(wsURL string, bizID, userID int64, stats *ClientStats, compressed bool) *WebSocketClient {
	return &WebSocketClient{
		wsURL:          wsURL,
		tokenGenerator: initTokenGenerator(),
		codec:          codec.NewJSONCodec(),
		encryptor:      initEncryptor(),
		bizID:          bizID,
		userID:         userID,
		stats:          stats,
		once:           sync.Once{},
		stopChan:       make(chan struct{}),
		compressed:     compressed,
	}
}

// initTokenGenerator 初始化token生成器
func initTokenGenerator() *jwt.UserToken {
	return jwt.NewUserToken(jwt.UserJWTKey, "mock")
}

// initEncryptor 初始化加密器
func initEncryptor() encrypt.Encryptor {
	// 使用与服务器相同的加密密钥
	encryptor, err := encrypt.NewAESEncryptor("1234567890abcdef1234567890abcdef")
	if err != nil {
		log.Fatalf("初始化加密器失败: %v", err)
	}
	return encryptor
}

// Connect 建立WebSocket连接
func (c *WebSocketClient) Connect(ctx context.Context) error {
	// 生成JWT token
	claims := jwt.UserClaims{
		UserID: c.userID,
		BizID:  c.bizID,
	}
	token, err := c.tokenGenerator.Encode(claims)
	if err != nil {
		return fmt.Errorf("生成token失败: %w", err)
	}

	// 构建WebSocket URL
	wsURL := fmt.Sprintf("%s?token=%s", c.wsURL, url.QueryEscape(token))

	// 建立WebSocket连接
	conn, _, _, err := ws.Dial(ctx, wsURL)
	if err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}

	c.conn = conn
	return nil
}

// Start 启动消息发送流程
//
//nolint:mnd // 忽略
func (c *WebSocketClient) Start(ctx context.Context, messagesPerSecond int, testMessage string) error {
	if c.conn == nil {
		return fmt.Errorf("客户端未连接")
	}

	c.stats.IncrementConnections()
	defer c.stats.DecrementConnections()

	// 启动消息循环
	go func() {
		if err := c.startMessageLoop(ctx); err != nil {
			log.Printf("客户端 %d 消息循环错误: %v", c.userID, err)
		}
	}()

	// 启动消息发送
	go func() {
		// 计算消息发送间隔，确保不会过于频繁
		interval := time.Duration(1000/messagesPerSecond) * time.Millisecond
		if interval < 50*time.Millisecond {
			interval = 50 * time.Millisecond // 最小间隔50ms
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.SendMessage(ctx, testMessage); err != nil {
					// 如果是连接错误，停止发送
					if isConnectionError(err) {
						return
					}
				}
			}
		}
	}()

	return nil
}

// isConnectionError 检查是否为连接相关错误
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "write: broken pipe") ||
		strings.Contains(errStr, "read: broken pipe")
}

// SendMessage 发送消息并上报统计
func (c *WebSocketClient) SendMessage(ctx context.Context, content string) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	err := c.SendUpstreamMessage(ctx, content)
	c.stats.IncrementMessages(err == nil)
	return err
}

// Stop 停止客户端
func (c *WebSocketClient) Stop() {
	c.once.Do(func() {
		// 发送停止信号
		close(c.stopChan)
		// 等待所有goroutine结束
		c.wg.Wait()
		// 线程安全地关闭连接
		c.connMutex.Lock()
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
		}
		c.connMutex.Unlock()
	})
}

// startMessageLoop 启动消息循环
//
//nolint:unparam // 忽略
func (c *WebSocketClient) startMessageLoop(ctx context.Context) error {
	// 启动心跳
	c.startHeartbeat(ctx)
	return nil
}

// sanitizeMessageContent 清理和验证消息内容
func (c *WebSocketClient) sanitizeMessageContent(content string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("消息内容不能为空")
	}

	// 检查内容长度
	if len(content) > 1024*1024 { // 1MB限制
		return "", fmt.Errorf("消息内容过长，最大支持1MB")
	}

	// 检查是否包含非法字符
	for i, char := range content {
		if char == 0 { // null字符
			return "", fmt.Errorf("消息内容包含非法字符null (位置: %d)", i)
		}
	}

	// 移除首尾空白字符
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("消息内容不能为空（去除空白后）")
	}

	return content, nil
}

// generateUpstreamMessage 生成上行消息
func (c *WebSocketClient) generateUpstreamMessage(content string) (*apiv1.Message, error) {
	// 清理和验证消息内容
	cleanContent, err := c.sanitizeMessageContent(content)
	if err != nil {
		return nil, fmt.Errorf("消息内容验证失败: %w", err)
	}

	// 业务消息
	businessMessage := &wrapperspb.StringValue{
		Value: cleanContent,
	}

	// 序列化业务消息
	bytes, err := protojson.Marshal(businessMessage)
	if err != nil {
		return nil, fmt.Errorf("序列化业务消息失败: %w", err)
	}

	// 验证序列化结果
	if len(bytes) == 0 {
		return nil, fmt.Errorf("序列化后的消息为空")
	}

	// 加密业务消息
	encryptedBody, err := c.encryptor.Encrypt(bytes)
	if err != nil {
		return nil, fmt.Errorf("加密业务消息失败: %w", err)
	}

	// 验证加密结果
	if len(encryptedBody) == 0 {
		return nil, fmt.Errorf("加密后的消息为空")
	}

	// 生成唯一key
	key := generateUniqueKey()

	// 构建上行网关消息
	upstreamMessage := &apiv1.Message{
		Cmd:  apiv1.Message_COMMAND_TYPE_UPSTREAM_MESSAGE,
		Key:  key,
		Body: encryptedBody,
	}

	return upstreamMessage, nil
}

// generateDownstreamAck 生成下行确认消息
func (c *WebSocketClient) generateDownstreamAck(key string) *apiv1.Message {
	return &apiv1.Message{
		Cmd: apiv1.Message_COMMAND_TYPE_DOWNSTREAM_ACK,
		Key: key,
	}
}

// generateHeartbeat 生成心跳消息
func (c *WebSocketClient) generateHeartbeat() *apiv1.Message {
	return &apiv1.Message{
		Cmd: apiv1.Message_COMMAND_TYPE_HEARTBEAT,
		Key: generateUniqueKey(),
	}
}

// generateUniqueKey 生成唯一key
func generateUniqueKey() string {
	const number16 = 16
	b := make([]byte, number16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// sendMessageSafe 并发安全的消息发送接口
func (c *WebSocketClient) sendMessageSafe(msg *apiv1.Message) error {
	c.connMutex.Lock()
	defer c.connMutex.Unlock()
	if c.conn == nil {
		return fmt.Errorf("连接关闭")
	}

	// 序列化消息
	payload, err := c.codec.Marshal(msg)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}

	// 每次发送都新建ClientWriter
	writer := NewClientWriter(c.conn, c.compressed)
	_, err = writer.Write(payload)
	if err != nil {
		return fmt.Errorf("发送消息失败: %w", err)
	}

	return nil
}

// receiveMessage 接收消息
func (c *WebSocketClient) receiveMessage() (*apiv1.Message, error) {
	c.connMutex.Lock()
	defer c.connMutex.Unlock()
	if c.conn == nil {
		return nil, fmt.Errorf("连接关闭")
	}
	// 读取消息 - 使用标准的WebSocket读取方式
	payload, _, err := wsutil.ReadServerData(c.conn)
	if err != nil {
		return nil, fmt.Errorf("读取消息失败: %w", err)
	}

	// 反序列化消息
	msg := &apiv1.Message{}
	err = c.codec.Unmarshal(payload, msg)
	if err != nil {
		return nil, fmt.Errorf("反序列化消息失败: %w", err)
	}

	return msg, nil
}

// handleMessage 处理接收到的消息
func (c *WebSocketClient) handleMessage(msg *apiv1.Message) error {
	switch msg.GetCmd() {
	case apiv1.Message_COMMAND_TYPE_UPSTREAM_ACK:
		return nil

	case apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE:
		// 发送下行确认
		ackMsg := c.generateDownstreamAck(msg.GetKey())
		return c.sendMessageSafe(ackMsg)

	case apiv1.Message_COMMAND_TYPE_HEARTBEAT:
		// 心跳消息不打印日志
		return nil

	default:
		log.Printf("收到未知消息类型: %v", msg.GetCmd())
		return nil
	}
}

// SendUpstreamMessage 发送上行消息并等待确认
func (c *WebSocketClient) SendUpstreamMessage(ctx context.Context, content string) error {
	// 生成上行消息
	const (
		time10 = 10 * time.Millisecond
		time5  = 5 * time.Millisecond
	)
	msg, err := c.generateUpstreamMessage(content)
	if err != nil {
		return fmt.Errorf("生成上行消息失败: %w", err)
	}

	// 发送消息
	err = c.sendMessageSafe(msg)
	if err != nil {
		return fmt.Errorf("发送上行消息失败: %w", err)
	}

	// 添加短暂延迟，确保消息完全发送
	time.Sleep(time10)

	// 等待确认（添加超时）
	ackCtx, ackCancel := context.WithTimeout(ctx, time5)
	defer ackCancel()

	ackMsg, err := c.receiveMessageWithTimeout(ackCtx)
	if err != nil {
		return fmt.Errorf("接收确认消息失败: %w", err)
	}

	// 处理确认消息
	return c.handleMessage(ackMsg)
}

// startHeartbeat 开始心跳
func (c *WebSocketClient) startHeartbeat(ctx context.Context) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				heartbeat := c.generateHeartbeat()
				if err := c.sendMessageSafe(heartbeat); err != nil {
					return
				}
			case <-c.stopChan:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// receiveMessageWithTimeout 带超时的消息接收
func (c *WebSocketClient) receiveMessageWithTimeout(ctx context.Context) (*apiv1.Message, error) {
	// 创建一个带缓冲的通道用于接收结果
	resultCh := make(chan struct {
		msg *apiv1.Message
		err error
	}, 1)

	go func() {
		msg, err := c.receiveMessage()
		resultCh <- struct {
			msg *apiv1.Message
			err error
		}{msg, err}
	}()

	select {
	case result := <-resultCh:
		return result.msg, result.err
	case <-ctx.Done():
		return nil, fmt.Errorf("接收消息超时: %w", ctx.Err())
	}
}
