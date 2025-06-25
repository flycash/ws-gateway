//go:build manual

package main

import (
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const (
	// 缓冲区大小配置
	readBufferSize  = 4096
	writeBufferSize = 4096
)

var (
	activeConnections int64
	totalMessages     int64
	startTime         = time.Now()

	// 缓冲区对象池
	bufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, readBufferSize)
		},
	}
)

// Connection 表示一个WebSocket连接，包含预分配的缓冲区
type Connection struct {
	conn        net.Conn
	readBuffer  []byte
	writeBuffer []byte
}

func main() {
	ln, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("监听端口失败: %v", err)
	}
	defer ln.Close()
	log.Println("WebSocket服务端启动 :50051")

	// 启动统计监控
	go monitorStats()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("接受连接失败: %v", err)
			continue
		}

		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	atomic.AddInt64(&activeConnections, 1)
	defer func() {
		atomic.AddInt64(&activeConnections, -1)
		conn.Close()
	}()

	// 升级为WebSocket连接
	_, err := ws.Upgrade(conn)
	if err != nil {
		log.Printf("升级WebSocket失败: %v", err)
		return
	}

	// 为连接预分配缓冲区
	wsConn := &Connection{
		conn:        conn,
		readBuffer:  bufferPool.Get().([]byte),
		writeBuffer: bufferPool.Get().([]byte),
	}

	// 确保在连接关闭时归还缓冲区到池中
	defer func() {
		bufferPool.Put(wsConn.readBuffer)
		bufferPool.Put(wsConn.writeBuffer)
	}()

	for {
		// 读取客户端消息
		_, op, err := wsutil.ReadClientData(conn)
		if err != nil {
			// 只在非正常关闭时打印错误
			if err.Error() != "EOF" {
				log.Printf("读取失败: %v", err)
			}
			return
		}

		// 增加消息计数
		atomic.AddInt64(&totalMessages, 1)

		// 使用预分配的写缓冲区构造响应
		response := wsConn.writeBuffer[:0] // 重置缓冲区
		response = append(response, []byte("OK")...)

		// 返回响应
		if err := wsutil.WriteServerMessage(conn, op, response); err != nil {
			log.Printf("发送失败: %v", err)
			return
		}
	}
}

func monitorStats() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		active := atomic.LoadInt64(&activeConnections)
		total := atomic.LoadInt64(&totalMessages)
		duration := time.Since(startTime).Seconds()
		throughput := float64(total) / duration

		log.Printf("统计 - 活跃连接: %d, 总消息: %d, 吞吐量: %.2f 消息/秒",
			active, total, throughput)
	}
}
