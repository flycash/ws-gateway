# WebSocket 客户端重构说明

## 重构概述

客户端代码已经重构为更简洁的架构，将 `WebSocketClient` 和 `internal.WebSocketClient` 合并为一个完整的客户端，主要特点：

1. **依赖注入**: `ClientStats` 实现通过依赖注入传入
2. **简化接口**: 只需要提供 WebSocket URL 和用户 ID
3. **自动连接管理**: 客户端自动处理连接建立和关闭
4. **统计上报**: 每次消息发送的成功/失败都会自动上报统计
5. **统一架构**: 不再需要包装器，直接使用 `internal.WebSocketClient`

## 核心组件

### WebSocketClient 结构体

```go
type WebSocketClient struct {
    wsURL          string                    // WebSocket服务器URL
    conn           net.Conn                  // 网络连接
    tokenGenerator *jwt.UserToken           // JWT token生成器
    codec          codec.Codec              // 消息编解码器
    encryptor      encrypt.Encryptor        // 消息加密器
    bizID          int64                     // 业务ID
    userID         int64                     // 用户ID
    stats          *ClientStats             // 统计实现（依赖注入）
    ctx            context.Context          // 上下文
    cancel         context.CancelFunc       // 取消函数
    wg             sync.WaitGroup           // 等待组
    once           sync.Once                // 一次性执行
    stopChan       chan struct{}            // 停止信号通道
}
```

### 主要方法

#### 1. NewWebSocketClient - 创建客户端
```go
func NewWebSocketClient(wsURL string, bizID, userID int64, stats *ClientStats) *WebSocketClient
```

#### 2. Connect - 建立连接
```go
func (c *WebSocketClient) Connect(ctx context.Context) error
```
- 自动生成JWT token
- 建立WebSocket连接
- 初始化内部组件

#### 3. Start - 启动消息发送流程
```go
func (c *WebSocketClient) Start(ctx context.Context, messagesPerSecond int, testMessage string) error
```
- 启动消息循环（处理接收）
- 启动消息发送（按指定频率）
- 启动心跳机制
- 监听context取消信号，自动退出

#### 4. SendMessage - 发送消息
```go
func (c *WebSocketClient) SendMessage(content string) error
```
- 发送消息并自动上报统计（成功/失败）

#### 5. Stop - 停止客户端
```go
func (c *WebSocketClient) Stop()
```
- 停止所有goroutine
- 关闭网络连接
- 清理资源

## 使用示例

### 基本使用

```go
// 1. 注入统计实现
stats := &internal.ClientStats{}
stats.StartTime = time.Now()

// 2. 创建客户端
client := internal.NewWebSocketClient("ws://localhost:50051/ws", 9999, 12345, stats)

// 3. 建立连接
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

if err := client.Connect(ctx); err != nil {
    log.Fatalf("连接失败: %v", err)
}

// 4. 启动消息发送流程
runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
defer runCancel()

if err := client.Start(runCtx, 1, "Hello WebSocket!"); err != nil {
    log.Fatalf("启动失败: %v", err)
}

// 5. 等待运行完成
<-runCtx.Done()

// 6. 停止客户端
client.Stop()

// 7. 查看统计信息
totalConn, activeConn, totalMsg, successMsg, failedMsg, duration := stats.GetStats()
fmt.Printf("总消息数: %d, 成功: %d, 失败: %d\n", totalMsg, successMsg, failedMsg)
```

### 压力测试使用

```bash
# 运行压力测试
go run main.go -clients=100 -mps=10 -duration=5m -server=ws://localhost:50051/ws
```

## 重构优势

1. **更简洁的架构**: 合并了两个客户端，减少了代码复杂度
2. **更简洁的API**: 只需要提供URL和用户ID，其他都由客户端内部处理
3. **更好的测试性**: 统计实现可以轻松替换为mock实现
4. **更清晰的职责**: 每个方法都有明确的单一职责
5. **更好的错误处理**: 连接失败、启动失败都有明确的错误返回
6. **自动资源管理**: 客户端自动处理连接的建立和关闭
7. **Context支持**: 支持优雅退出和超时控制
8. **内置功能**: 自动心跳、消息加密、JWT认证等

## 内置功能

### 1. 自动心跳
客户端会自动发送心跳消息保持连接活跃

### 2. 消息加密
使用AES加密算法对消息进行加密

### 3. JWT认证
自动生成和验证JWT token

### 4. 消息确认
支持上行消息的确认机制

### 5. 下行消息处理
自动处理下行消息并发送确认

### 6. 连接监控和自动重连
- 定期检查连接状态
- 自动检测连接断开
- 支持自动重连（最多3次，指数退避）
- 优雅处理 "broken pipe" 等连接错误

### 7. 错误处理和超时控制
- 连接错误自动检测和恢复
- 消息发送超时控制（5秒）
- 连接状态检查
- 防止向已断开连接发送消息

## 统计信息

客户端会自动收集以下统计信息：

- `totalConnections`: 总连接数
- `activeConnections`: 活跃连接数
- `totalMessages`: 总消息数
- `successMessages`: 成功消息数
- `failedMessages`: 失败消息数
- `StartTime`: 开始时间

每次调用 `SendMessage` 都会自动更新成功/失败统计。

## 架构对比

### 重构前
```
main.go -> WebSocketClientWrapper -> internal.WebSocketClient
```

### 重构后
```
main.go -> internal.WebSocketClient (合并后)
```

重构后的架构更加简洁，减少了不必要的包装层，提高了代码的可维护性和性能。

# WebSocket 客户端使用说明

## 概述

这是一个用于测试 WebSocket Gateway 的客户端工具，支持多客户端并发连接、消息发送和统计信息收集。

## 功能特性

- **多客户端并发**: 支持同时运行多个 WebSocket 客户端
- **消息频率控制**: 可配置每秒发送消息数量
- **统计信息收集**: 自动收集连接数、消息数等统计信息
- **连接管理**: 自动处理连接建立、断开和重连
- **消息验证**: 自动验证和清理消息内容，防止乱码
- **调试模式**: 支持详细的消息传输日志
- **错误处理**: 完善的错误处理和恢复机制

## 使用方法

### 基本使用

```bash
# 运行基本测试
go run main.go

# 自定义参数
go run main.go -clients=50 -mps=10 -duration=5m -server=ws://localhost:50051/ws
```

### 参数说明

- `-clients`: 客户端连接数量 (默认: 10)
- `-mps`: 每秒发送消息数量 (默认: 1)
- `-duration`: 测试持续时间 (默认: 1分钟)
- `-conn-timeout`: 连接超时时间 (默认: 10秒)
- `-msg-size`: 消息大小，字节 (默认: 100)
- `-server`: WebSocket服务器地址 (默认: ws://localhost:50051/ws)
- `-debug`: 启用调试模式 (默认: false)

### 调试模式

启用调试模式可以查看详细的消息传输信息：

```bash
# 启用调试模式
go run main.go -debug=true -clients=1 -mps=1
```

调试模式会输出：
- 消息的详细内容（仅短消息）
- 消息的序列化和加密过程
- 连接状态变化
- 错误详情

## 消息传输问题排查

### 重要发现：Debug模式解决乱码问题

**现象**：开启debug模式后，服务端接收乱码的现象消失。

**根本原因**：这是一个**时序竞争条件(Race Condition)**问题。在非debug模式下，客户端和服务端的消息处理存在时序竞争，导致WebSocket帧传输不稳定。

**技术分析**：

1. **时序竞争条件**：
   - 客户端快速发送消息时，WebSocket连接可能还未完全稳定
   - 消息发送和接收之间存在微妙的时序依赖
   - 高频率的消息发送可能导致帧传输不完整

2. **Debug模式的影响**：
   - 日志输出增加了消息处理延迟
   - 序列化验证操作给了连接更多稳定时间
   - 减少了消息发送和接收之间的竞争

3. **解决方案**：
   - 添加连接状态检查，确保连接稳定后再发送
   - 增加消息发送间隔控制（最小50ms）
   - 添加消息发送后的短暂延迟（10ms）
   - 设置写超时和缓冲区管理

**代码改进**：
```go
// 连接状态检查
if !c.isConnectionAlive() {
    return fmt.Errorf("连接状态不稳定")
}

// 消息发送间隔控制
interval := time.Duration(1000/messagesPerSecond) * time.Millisecond
if interval < 50*time.Millisecond {
    interval = 50 * time.Millisecond // 最小间隔50ms
}

// 消息发送后延迟
time.Sleep(10 * time.Millisecond)
```

### 常见问题

#### 1. 服务端接收乱码

**问题分析**:
服务端接收乱码通常是由于 WebSocket 帧格式不匹配或时序竞争条件导致的。客户端和服务端使用不同的 WebSocket 读写方式可能导致数据损坏。

**根本原因**:
1. **WebSocket 帧格式不匹配**: 客户端和服务端使用不同的 WebSocket 库或读写方式
2. **WebSocket 状态不一致**: 客户端和服务端的 WebSocket 状态设置不匹配
3. **压缩扩展问题**: 服务端启用了压缩但客户端没有正确处理
4. **掩码处理问题**: 客户端发送的帧没有正确应用掩码
5. **时序竞争条件**: 消息发送频率过高导致连接不稳定

**解决方案**:

1. **启用调试模式**查看详细的消息传输过程:
   ```bash
   go run main.go -debug=true -clients=1 -mps=1
   ```

2. **控制消息发送频率**:
   ```bash
   # 使用较低的消息频率
   go run main.go -clients=10 -mps=5
   
   # 避免过高的消息频率
   # go run main.go -clients=100 -mps=100  # 不推荐
   ```

3. **检查服务端压缩配置**:
   确保服务端配置文件中的压缩设置与客户端兼容：
   ```yaml
   server:
     websocket:
       compression:
         enabled: false  # 暂时禁用压缩进行测试
   ```

4. **验证 WebSocket 连接**:
   使用浏览器开发者工具或 WebSocket 客户端工具测试连接：
   ```javascript
   // 浏览器控制台测试
   const ws = new WebSocket('ws://localhost:50051/ws?token=your-token');
   ws.onmessage = function(event) {
       console.log('收到消息:', event.data);
   };
   ws.send('{"cmd":2,"key":"test","body":"test-data"}');
   ```

5. **检查网络代理和防火墙**:
   - 确保没有代理服务器修改 WebSocket 数据
   - 检查防火墙是否阻止了 WebSocket 连接
   - 验证端口是否被正确开放

6. **使用 Wireshark 抓包分析**:
   如果问题持续存在，可以使用 Wireshark 抓取 WebSocket 数据包进行分析：
   ```bash
   # 过滤 WebSocket 流量
   wireshark -f "tcp port 50051"
   ```

**调试步骤**:

1. **基本连接测试**:
   ```bash
   # 测试基本连接
   go run main.go -debug=true -clients=1 -mps=1 -duration=30s
   ```

2. **检查服务端日志**:
   查看服务端是否报告了 WebSocket 相关的错误：
   - "从客户端读取消息失败"
   - "反序列化消息失败"
   - "解密消息体失败"

3. **验证消息格式**:
   启用调试模式后，检查客户端发送的消息格式是否正确：
   - 序列化后的 JSON 格式
   - 消息长度是否合理
   - 是否包含非法字符

4. **测试不同消息大小**:
   ```bash
   # 测试小消息
   go run main.go -debug=true -clients=1 -mps=1 -msg-size=10
   
   # 测试大消息
   go run main.go -debug=true -clients=1 -mps=1 -msg-size=1000
   ```

**常见错误及解决方案**:

#### 错误: "从客户端读取消息失败"
- **原因**: WebSocket 帧格式不匹配或连接已断开
- **解决**: 检查 WebSocket 库版本和配置

#### 错误: "反序列化消息失败"
- **原因**: 接收到的数据不是有效的 JSON 格式
- **解决**: 检查 WebSocket 帧是否正确传输

#### 错误: "解密消息体失败"
- **原因**: 加密密钥不匹配或数据在传输过程中损坏
- **解决**: 验证加密密钥和 WebSocket 传输

**临时解决方案**:

如果问题持续存在，可以尝试以下临时解决方案：

1. **禁用压缩**:
   ```yaml
   server:
     websocket:
       compression:
         enabled: false
   ```

2. **使用不同的序列化格式**:
   ```yaml
   server:
     websocket:
       serializer: "proto"  # 尝试使用 protobuf 格式
   ```

3. **禁用加密**:
   ```yaml
   server:
     websocket:
       encrypt:
         enabled: false
   ```

**预防措施**:

1. **统一 WebSocket 库版本**: 确保客户端和服务端使用相同版本的 WebSocket 库
2. **标准化消息格式**: 使用标准的 JSON 或 protobuf 格式
3. **添加消息校验**: 在消息中添加校验和或签名
4. **监控网络质量**: 监控网络延迟和丢包率
5. **定期测试**: 定期进行端到端测试验证消息传输
6. **控制消息频率**: 避免过高的消息发送频率，给连接足够的稳定时间

#### 2. 连接断开

**原因**:
- 网络不稳定
- 服务端主动断开连接
- 心跳超时
- 消息发送频率过高

**解决方案**:
- 客户端已内置自动重连机制
- 调整消息发送频率
- 检查网络连接稳定性

#### 3. 消息发送失败

**原因**:
- 连接已断开
- 消息内容过长
- 消息包含非法字符
- 序列化失败

**解决方案**:
- 客户端会自动验证消息内容
- 检查消息长度限制（默认1MB）
- 确保消息不包含null字符

### 调试步骤

1. **启用调试模式**:
   ```bash
   go run main.go -debug=true -clients=1 -mps=1
   ```

2. **检查日志输出**:
   - 查看消息的原始内容和长度
   - 检查消息的序列化结果
   - 验证加密后的消息长度
   - 确认序列化验证是否成功

3. **验证消息格式**:
   - 确保消息符合 protobuf 格式
   - 检查 JWT token 是否正确
   - 验证加密密钥是否匹配

4. **网络诊断**:
   - 使用 `ping` 检查网络连通性
   - 使用 `telnet` 测试端口连通性
   - 检查防火墙设置

### 服务端配置检查

确保服务端配置文件包含以下设置：

```yaml
server:
  websocket:
    # 序列化协议：必须与客户端一致
    serializer: "json"
    
    # 消息体加密配置
    encrypt:
      enabled: true
      algorithm: "aes"
      # 密钥必须与客户端一致
      key: "1234567890abcdef1234567890abcdef"
```

### 消息流程验证

客户端发送消息的完整流程：

1. **消息内容验证**: 清理和验证输入内容
2. **业务消息序列化**: 使用 `protojson.Marshal` 序列化
3. **消息体加密**: 使用 AES-GCM 加密
4. **网关消息构建**: 创建包含命令类型、Key和加密体的消息
5. **网关消息序列化**: 使用 JSON 序列化整个消息
6. **WebSocket发送**: 通过 WebSocket 发送二进制数据

服务端接收消息的完整流程：

1. **WebSocket接收**: 接收二进制数据
2. **网关消息反序列化**: 使用 JSON 反序列化
3. **消息体解密**: 使用 AES-GCM 解密
4. **业务消息反序列化**: 使用 `protojson.Unmarshal` 反序列化
5. **业务处理**: 处理业务逻辑

### 常见错误及解决方案

#### 错误: "解密消息体失败"
- **原因**: 加密密钥不匹配
- **解决**: 检查客户端和服务端的加密密钥是否一致

#### 错误: "反序列化消息失败"
- **原因**: 序列化格式不匹配
- **解决**: 确保客户端和服务端使用相同的序列化格式（JSON）

#### 错误: "消息内容验证失败"
- **原因**: 消息包含非法字符或为空
- **解决**: 检查消息内容，确保不包含null字符

#### 错误: "broken pipe"
- **原因**: 连接已断开
- **解决**: 客户端会自动重连，这是正常现象

## 统计信息

客户端会自动收集以下统计信息：

- **总连接数**: 累计建立的连接数
- **活跃连接数**: 当前活跃的连接数
- **总消息数**: 累计发送的消息数
- **成功消息数**: 发送成功的消息数
- **失败消息数**: 发送失败的消息数
- **测试持续时间**: 从开始到结束的时间

## 错误处理

### 连接错误
- 自动重连机制（最多3次，指数退避）
- 连接状态监控
- 优雅的错误恢复

### 消息错误
- 消息内容验证
- 自动清理非法字符
- 长度限制检查

### 超时处理
- 连接超时控制
- 消息发送超时
- 心跳超时检测

## 性能优化建议

1. **合理设置客户端数量**: 根据服务器性能调整
2. **控制消息频率**: 避免过高的消息发送频率
3. **监控资源使用**: 注意内存和CPU使用情况
4. **网络优化**: 确保网络连接稳定

## 故障排除

### 如果遇到 "broken pipe" 错误

这是正常的连接断开错误，客户端会自动处理：
- 检测连接状态
- 自动重连
- 停止向已断开连接发送消息

### 如果服务端报错

1. 检查服务端日志
2. 启用客户端调试模式
3. 验证消息格式和加密密钥
4. 检查网络连接

### 如果性能不佳

1. 减少客户端数量
2. 降低消息发送频率
3. 检查网络带宽
4. 监控服务器资源使用

### 如果服务端接收乱码

1. **启用调试模式**查看详细的消息传输过程
2. **检查加密密钥**是否与服务端一致
3. **验证序列化格式**是否与服务端匹配
4. **检查消息内容**是否包含非法字符
5. **查看服务端日志**了解具体的错误信息

### 调试命令示例

```bash
# 基本调试
go run main.go -debug=true -clients=1 -mps=1 -duration=30s

# 压力测试调试
go run main.go -debug=true -clients=10 -mps=5 -duration=2m

# 长消息测试
go run main.go -debug=true -clients=1 -mps=1 -msg-size=1000
``` 