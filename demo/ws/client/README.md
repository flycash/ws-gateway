# WebSocket Client

这个WebSocket客户端表示一个连接的生命周期，负责处理已建立连接的消息发送和接收。

## 设计理念

- **连接生命周期管理**: WebSocketClient不再负责建立连接，而是接受一个已建立的WebSocket连接作为初始化参数
- **消息循环**: 通过`Loop()`方法启动持续的消息处理循环
- **分离关注点**: 连接建立和消息处理分离，上层负责连接管理，WebSocketClient负责消息处理

## 使用方法

### 1. 建立WebSocket连接

```go
// 生成JWT token
tokenGenerator := jwt.NewUserToken(jwt.UserJWTKey, "mock")
claims := jwt.UserClaims{
    UserID: 12345,
    BizID:  9999,
}
token, err := tokenGenerator.Encode(claims)
if err != nil {
    log.Fatalf("生成token失败: %v", err)
}

// 构建WebSocket URL
serverURL := "ws://localhost:9002/ws"
wsURL := fmt.Sprintf("%s?token=%s", serverURL, url.QueryEscape(token))

// 建立WebSocket连接
conn, _, _, err := ws.Dial(context.Background(), wsURL)
if err != nil {
    log.Fatalf("连接WebSocket失败: %v", err)
}
defer conn.Close()
```

### 2. 创建WebSocketClient

```go
// 创建WebSocket客户端（传入已建立的连接）
client := internal.NewWebSocketClient(conn, 9999, 12345)
```

### 3. 启动消息循环

```go
// 启动消息循环（在goroutine中运行）
go func() {
    ctx := context.Background() // 或者使用带超时的上下文
    if err := client.Start(ctx); err != nil {
        log.Printf("消息循环错误: %v", err)
    }
}()
```

// 或者使用带超时的上下文
```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

if err := client.Start(ctx); err != nil {
    log.Printf("消息循环错误: %v", err)
}
```

### 4. 发送消息

```go
// 发送上行消息
content := "Hello from client!"
if err := client.SendUpstreamMessage(content); err != nil {
    log.Printf("发送上行消息失败: %v", err)
}
```

### 5. 停止客户端

```go
// 停止客户端
client.Stop()
```

## 压力测试工具

项目包含一个压力测试工具，支持以下命令行参数：

### 命令行参数

- `-clients`: 客户端连接数量 (默认: 10)
- `-mps`: 每秒发送消息数量 (默认: 1)
- `-duration`: 测试持续时间 (默认: 1分钟)
- `-conn-timeout`: 连接超时时间 (默认: 10秒)
- `-resp-timeout`: 响应超时时间 (默认: 1秒)
- `-ramp-up`: 连接建立阶段时间 (默认: 10秒)
- `-msg-size`: 消息大小(字节) (默认: 100)
- `-server`: WebSocket服务器地址 (默认: ws://localhost:9002/ws)

### 使用示例

```bash
# 基本测试：10个客户端，每秒1条消息，测试1分钟
go run demo/ws/client/example/main.go

# 高并发测试：1000个客户端，每秒100条消息，测试5分钟
go run demo/ws/client/example/main.go -clients=1000 -mps=100 -duration=5m

# 大消息测试：100个客户端，每秒10条消息，消息大小1KB
go run demo/ws/client/example/main.go -clients=100 -mps=10 -msg-size=1024

# 快速连接测试：快速建立连接，短时间测试
go run demo/ws/client/example/main.go -clients=500 -ramp-up=5s -duration=30s
```

### 统计信息

压力测试工具会定期输出以下统计信息：

- 总连接数
- 活跃连接数
- 总消息数
- 成功消息数
- 失败消息数
- 运行时间
- 消息速率 (msg/s)
- 成功率 (%)

## 主要方法

- `NewWebSocketClient(conn net.Conn, bizID, userID int64) *WebSocketClient`: 创建新的WebSocket客户端
- `Start(ctx context.Context) error`: 启动消息循环，持续处理消息的发送和接收，可通过context控制结束
- `SendUpstreamMessage(content string) error`: 发送上行消息并等待确认
- `Stop()`: 停止客户端，关闭连接

## 特性

- **自动心跳**: 客户端会自动发送心跳消息保持连接活跃
- **消息确认**: 支持上行消息的确认机制
- **下行消息处理**: 自动处理下行消息并发送确认
- **优雅关闭**: 支持优雅停止，等待所有goroutine结束
- **压力测试**: 内置压力测试工具，支持多种参数配置
- **实时统计**: 提供详细的连接和消息统计信息

## 示例

完整的使用示例请参考 `example/main.go` 文件。 