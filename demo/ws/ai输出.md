设计一个针对使用 `github.com/gobwas/ws` 的 WebSocket 服务端的性能测试方案，目标是找出系统的瓶颈和最大承载能力。以下是一个**系统性测试方案设计**，从控制变量、测试维度、数据采集、工具选型等角度详细展开：

---

## 🧩 一、测试目标

* 测试服务器能承载的最大连接数
* 测试消息吞吐能力（TPS/消息大小）
* 衡量延迟（RTT）
* 观察在不同配置下 CPU、内存、网络资源使用情况

---

## 📌 二、可控变量分类

### 1. **TCP 层参数**

可通过 `sysctl` 等调整内核参数影响 TCP 性能：

| 参数名                        | 含义               | 建议测试取值              |
| -------------------------- | ---------------- | ------------------- |
| `net.core.somaxconn`       | TCP backlog 队列长度 | 默认 128 → 512 → 1024 |
| `net.ipv4.tcp_tw_reuse`    | TIME\_WAIT 重用    | 0/1                 |
| `net.ipv4.tcp_fin_timeout` | FIN\_WAIT 超时     | 默认 → 10s            |
| `ulimit -n`                | 最大文件描述符数         | 1024 → 65535        |

### 2. **`gobwas/ws` 参数**

这个库底层比较轻量，没有过多配置项，但你可以**间接控制的有：**

| 控制点              | 含义                                 | 示例                       |
| ---------------- | ---------------------------------- | ------------------------ |
| Read buffer size | 读取缓冲区大小                            | 自定义 `bufio.Reader` 传入    |
| 并发模型             | 每连接是否使用 goroutine、是否使用 epoll（自行实现） | goroutine per connection |
| 写策略              | 消息是否批量发送                           | 批量 vs 单发                 |

### 3. **连接数**

| 参数    | 范围                        |
| ----- | ------------------------- |
| 并发连接数 | 1K, 10K, 50K, 100K（依机器资源） |

### 4. **消息频率与大小**

| 变量   | 范围                   |
| ---- | -------------------- |
| 消息频率 | 每连接每秒 1、5、10、50 条    |
| 消息大小 | 128B, 512B, 1KB, 4KB |

---

## 🛠️ 三、性能测试工具

### 客户端模拟器选择：

#### ✅ 推荐：自定义客户端（Go 编写）

用 Go 写客户端能更好控制 gobwas/ws 兼容性、内存模型一致。

示例：

```go
for i := 0; i < totalClients; i++ {
go func() {
conn, _, _, err := ws.Dial(context.Background(), "ws://localhost:8080/ws")
// 心跳、消息发送、读响应...
}()
}
```

也可使用协程池/限流控制压力。

#### ✅ 可选：第三方工具

| 工具                                                      | 特点                    |
| ------------------------------------------------------- | --------------------- |
| [wrk-ws](https://github.com/dhoelzgen/wrk-ws)           | 基于 Lua 脚本，继承 `wrk` 性能 |
| [bombardier](https://github.com/codesenberg/bombardier) | 支持 WebSocket          |
| [artillery](https://artillery.io/)                      | JS 脚本化，易用             |

---

## 📈 四、测试指标采集

| 维度            | 指标项                              |
| ------------- | -------------------------------- |
| 服务端资源         | CPU, 内存, Goroutine 数, GC 次数，网络带宽 |
| 网络            | RTT（ping-pong 时间）、丢包率、连接建立成功率    |
| WebSocket 应用层 | QPS、TPS、消息延迟、连接稳定性               |
| 错误率           | 握手失败率、消息失败率、异常断开连接数              |

### 📋 采集方式

* Go 程序内嵌 `/metrics`，Prometheus + Grafana 观测
* `netstat`, `ss`, `top`, `htop`, `iftop`, `perf` 等系统工具
* 自定义日志记录（每连接发送/接收耗时）

---

## 🔁 五、测试策略设计

### 📶 1. 连接数逐步拉升

* 从 1000 开始，每次加 1000，观察性能指标变化

### 🔁 2. 发送频率/消息大小组合测试

| 场景     | 连接数 | 频率   | 消息大小 |
| ------ | --- | ---- | ---- |
| 高并发低消息 | 10K | 1/s  | 256B |
| 中并发中消息 | 5K  | 5/s  | 1KB  |
| 低并发高消息 | 1K  | 50/s | 4KB  |

### 📉 3. 极限测试

* 开最大连接数直到 OOM、GC 抖动、延迟陡增为止，找服务极限

---

## 🧪 六、结果分析与优化方向

| 现象      | 可能优化方向                              |
| ------- | ----------------------------------- |
| GC 次数频繁 | 减少内存分配，如复用 buffer                   |
| CPU 飙升  | 优化 epoll/goroutine 使用；批量写           |
| 高 RTT   | 减少上下文切换，合并写                         |
| 连接数受限   | 增大 ulimit、somaxconn、减少 goroutine 堆栈 |

---

## 📦 示例目录结构建议

```bash
loadtest/
  client/
    main.go          # 模拟器
    config.yaml      # 控制连接数、频率等
  scripts/
    system_tune.sh   # ulimit & sysctl 调整脚本
  results/
    run_2025-06-25.json
    charts/
```

---

## ✅ 总结：你需要做的

1. 用 Go 写一个轻量级客户端模拟器（保持和 gobwas/ws 一致）
2. 用 YAML/JSON 控制测试场景
3. 编写 shell 脚本统一环境设置（ulimit, sysctl）
4. Prometheus + Grafana 收集关键指标
5. 控制变量逐一测试，并记录曲线图

---

如需我帮你写一个简单的客户端模拟器，或性能测试脚本原型，也可以告诉我。

