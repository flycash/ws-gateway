package internal

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"gitee.com/flycash/ws-gateway/pkg/encrypt"
	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"github.com/gobwas/ws/wsutil"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// WebSocketClient WebSocket客户端，表示一个连接的生命周期
type WebSocketClient struct {
	conn           net.Conn
	tokenGenerator *jwt.UserToken
	codec          codec.Codec
	encryptor      encrypt.Encryptor
	bizID          int64
	userID         int64
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	stopChan       chan struct{}
}

// NewWebSocketClient 创建新的WebSocket客户端
// conn: 已建立的WebSocket连接
// bizID: 业务ID
// userID: 用户ID
func NewWebSocketClient(conn net.Conn, bizID, userID int64) *WebSocketClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &WebSocketClient{
		conn:           conn,
		tokenGenerator: initTokenGenerator(),
		codec:          codec.NewJSONCodec(),
		encryptor:      initEncryptor(),
		bizID:          bizID,
		userID:         userID,
		ctx:            ctx,
		cancel:         cancel,
		stopChan:       make(chan struct{}),
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

// generateUpstreamMessage 生成上行消息
func (c *WebSocketClient) generateUpstreamMessage(content string) (*apiv1.Message, error) {
	// 业务消息
	businessMessage := &wrapperspb.StringValue{
		Value: content,
	}

	// 序列化业务消息
	bytes, err := protojson.Marshal(businessMessage)
	if err != nil {
		return nil, fmt.Errorf("序列化业务消息失败: %v", err)
	}

	// 加密业务消息
	encryptedBody, err := c.encryptor.Encrypt(bytes)
	if err != nil {
		return nil, fmt.Errorf("加密业务消息失败: %v", err)
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
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// sendMessage 发送消息
func (c *WebSocketClient) sendMessage(msg *apiv1.Message) error {
	// 序列化消息
	payload, err := c.codec.Marshal(msg)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %v", err)
	}

	// 发送消息
	err = wsutil.WriteClientBinary(c.conn, payload)
	if err != nil {
		return fmt.Errorf("发送消息失败: %v", err)
	}

	log.Printf("发送消息: %s", string(payload))
	return nil
}

// receiveMessage 接收消息
func (c *WebSocketClient) receiveMessage() (*apiv1.Message, error) {
	// 读取消息
	payload, err := wsutil.ReadServerBinary(c.conn)
	if err != nil {
		return nil, fmt.Errorf("读取消息失败: %v", err)
	}

	// 反序列化消息
	msg := &apiv1.Message{}
	err = c.codec.Unmarshal(payload, msg)
	if err != nil {
		return nil, fmt.Errorf("反序列化消息失败: %v", err)
	}

	log.Printf("接收消息: %s", string(payload))
	return msg, nil
}

// handleMessage 处理接收到的消息
func (c *WebSocketClient) handleMessage(msg *apiv1.Message) error {
	switch msg.GetCmd() {
	case apiv1.Message_COMMAND_TYPE_UPSTREAM_ACK:
		log.Printf("收到上行确认消息，key: %s", msg.GetKey())
		return nil

	case apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE:
		log.Printf("收到下行消息，key: %s", msg.GetKey())
		// 发送下行确认
		ackMsg := c.generateDownstreamAck(msg.GetKey())
		return c.sendMessage(ackMsg)

	case apiv1.Message_COMMAND_TYPE_HEARTBEAT:
		log.Printf("收到心跳消息")
		return nil

	default:
		log.Printf("收到未知消息类型: %v", msg.GetCmd())
		return nil
	}
}

// SendUpstreamMessage 发送上行消息并等待确认
func (c *WebSocketClient) SendUpstreamMessage(content string) error {
	// 生成上行消息
	msg, err := c.generateUpstreamMessage(content)
	if err != nil {
		return fmt.Errorf("生成上行消息失败: %v", err)
	}

	// 发送消息
	err = c.sendMessage(msg)
	if err != nil {
		return fmt.Errorf("发送上行消息失败: %v", err)
	}

	// 等待确认
	ackMsg, err := c.receiveMessage()
	if err != nil {
		return fmt.Errorf("接收确认消息失败: %v", err)
	}

	// 处理确认消息
	return c.handleMessage(ackMsg)
}

// startHeartbeat 开始心跳
func (c *WebSocketClient) startHeartbeat() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				heartbeat := c.generateHeartbeat()
				if err := c.sendMessage(heartbeat); err != nil {
					log.Printf("发送心跳失败: %v", err)
					return
				}
			case <-c.stopChan:
				return
			case <-c.ctx.Done():
				return
			}
		}
	}()
}

// startMessageReceiver 开始消息接收器
func (c *WebSocketClient) startMessageReceiver() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for {
			select {
			case <-c.stopChan:
				return
			case <-c.ctx.Done():
				return
			default:
				msg, err := c.receiveMessage()
				if err != nil {
					log.Printf("接收消息失败: %v", err)
					return
				}
				if err := c.handleMessage(msg); err != nil {
					log.Printf("处理消息失败: %v", err)
				}
			}
		}
	}()
}

// Loop 启动消息循环，持续处理消息的发送和接收
func (c *WebSocketClient) Loop() error {
	log.Printf("WebSocket客户端开始消息循环")

	// 启动心跳
	c.startHeartbeat()

	// 启动消息接收器
	c.startMessageReceiver()

	// 等待停止信号
	<-c.stopChan

	log.Printf("WebSocket客户端消息循环结束")
	return nil
}

// Start 启动客户端（保持向后兼容）
func (c *WebSocketClient) Start(ctx context.Context) error {
	log.Printf("WebSocket客户端开始消息循环")

	// 启动心跳
	c.startHeartbeat()

	// 启动消息接收器
	c.startMessageReceiver()

	// 等待停止信号或上下文取消
	select {
	case <-c.stopChan:
		log.Printf("收到停止信号")
	case <-ctx.Done():
		log.Printf("上下文已取消")
		c.Stop()
	case <-c.ctx.Done():
		log.Printf("客户端上下文已取消")
	}

	log.Printf("WebSocket客户端消息循环结束")
	return nil
}

// Stop 停止客户端
func (c *WebSocketClient) Stop() {
	log.Printf("正在停止WebSocket客户端...")

	// 发送停止信号
	close(c.stopChan)

	// 取消上下文
	c.cancel()

	// 等待所有goroutine结束
	c.wg.Wait()

	// 关闭连接
	if c.conn != nil {
		c.conn.Close()
	}

	log.Printf("WebSocket客户端已停止")
}

// RunCommunicationFlow 运行通信流程
func (c *WebSocketClient) RunCommunicationFlow() {
	// 创建上下文，5秒后自动取消
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 启动消息循环
	if err := c.Start(ctx); err != nil {
		log.Fatalf("启动消息循环失败: %v", err)
	}
	defer c.Stop()

	// 发送上行消息
	content := fmt.Sprintf("Hello from User%d, round %d", c.userID, 1)
	if err := c.SendUpstreamMessage(content); err != nil {
		log.Printf("发送上行消息失败: %v", err)
	}

	log.Printf("通信流程完成")
}

// Run 运行客户端示例（需要上层先建立连接）
func Run() {
	// 注意：这里需要上层先建立WebSocket连接
	// 示例代码，实际使用时需要先建立连接
	log.Printf("请先建立WebSocket连接，然后传入连接对象")

	// 示例：如何建立连接并创建客户端
	/*
		// 建立WebSocket连接
		conn, _, _, err := ws.Dial(context.Background(), "ws://localhost:9002/ws?token=xxx")
		if err != nil {
			log.Fatalf("连接WebSocket失败: %v", err)
		}

		// 创建客户端
		client := NewWebSocketClient(conn, 9999, 12345)

		// 运行通信流程
		client.RunCommunicationFlow()
	*/
}
