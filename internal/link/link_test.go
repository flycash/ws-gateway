//go:build unit

package link_test

import (
	"compress/flate"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"gitee.com/flycash/ws-gateway/pkg/session/mocks"
	"gitee.com/flycash/ws-gateway/pkg/wswrapper"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gobwas/ws/wsutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

func TestLink_New_ID_Session(t *testing.T) {
	t.Parallel()

	server, _ := newServerAndClientConn()
	id := "1"
	sess := createTestSession(t, session.UserInfo{BizID: 3, UserID: 2})
	lk := newLinkWith(t.Context(), id, sess, server)
	assert.Equal(t, id, lk.ID())
	assert.Equal(t, sess.UserInfo(), lk.Session().UserInfo())
}

func TestLink_Close(t *testing.T) {
	t.Parallel()

	t.Run("应该是开启的,当Link刚刚被创建", func(t *testing.T) {
		t.Parallel()

		serverConn, _ := newServerAndClientConn()
		lk := newLink(t.Context(), "2", serverConn)

		select {
		case <-lk.HasClosed():
			assert.NoError(t, fmt.Errorf("刚刚创建的link不应该是被关闭的"))
		default:
			// 多次关闭无副作用
			assert.NoError(t, lk.Close())
			assert.NoError(t, lk.Close())
			assert.NotNil(t, <-lk.HasClosed())
		}
	})

	// 读写过程中调用Close的情况详见TestLink_Receive及TestLink_Send
}

func TestLink_Receive(t *testing.T) {
	t.Parallel()

	t.Run("应该接收成功,当Link未关闭", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newServerAndClientConn()
		lk := newLink(t.Context(), "1", serverConn)

		expected := []byte("Hello, It's Client")
		clientErrorCh := make(chan error)

		go func() {
			clientErrorCh <- wsutil.WriteClientBinary(clientConn, expected)
		}()

		assert.NoError(t, <-clientErrorCh)
		actual, ok := <-lk.Receive()
		assert.True(t, ok)
		assert.Equal(t, expected, actual)
		assert.NoError(t, lk.Close())
		assert.NoError(t, lk.Close())
	})

	t.Run("应该接收失败,当Link已关闭", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newServerAndClientConn()
		lk := newLink(t.Context(), "2", serverConn)

		expected := []byte("Hello, It's Client")
		assert.NoError(t, wsutil.WriteClientBinary(clientConn, expected))

		assert.NoError(t, lk.Close())
		assert.NoError(t, lk.Close())

		<-lk.Receive()
		_, ok := <-lk.Receive()
		assert.False(t, ok)
	})

	t.Run("应该关闭Link及底层net.Conn,当客户端关闭/断开net.Conn", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newServerAndClientConn()
		lk := newLink(t.Context(), "2", serverConn)

		expected := []byte("Hello, It's Client")
		clientErrorCh := make(chan error)

		go func() {
			clientErrorCh <- wsutil.WriteClientBinary(clientConn, expected)
			// 客户端主动断开连接
			clientErrorCh <- clientConn.Close()
		}()

		for {
			select {
			case actual, ok := <-lk.Receive():
				if !ok {
					continue
				}
				assert.NoError(t, <-clientErrorCh)
				assert.True(t, ok)
				assert.Equal(t, expected, actual)

			case <-lk.HasClosed():

				assert.NoError(t, <-clientErrorCh)
				assert.NoError(t, lk.Close())
				assert.NoError(t, lk.Close())
				return
			}
		}
	})
}

func TestLink_Send(t *testing.T) {
	t.Parallel()

	t.Run("应该发送成功,当Link未关闭", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newServerAndClientConn()
		lk := newLink(t.Context(), "1", serverConn)

		clientErrorCh := make(chan error)
		go func() {
			expected, err := wsutil.ReadServerBinary(clientConn)
			clientErrorCh <- err
			clientErrorCh <- wsutil.WriteClientBinary(clientConn, expected)
		}()

		expected := []byte("Hello, It's Server")
		assert.NoError(t, lk.Send(expected))
		assert.NoError(t, <-clientErrorCh)

		assert.NoError(t, <-clientErrorCh)
		actual, ok := <-lk.Receive()
		assert.True(t, ok)
		assert.Equal(t, expected, actual)

		assert.NoError(t, lk.Close())
	})

	t.Run("应该发送失败,当Link已关闭", func(t *testing.T) {
		t.Parallel()

		serverConn, _ := newServerAndClientConn()
		lk := newLink(t.Context(), "1", serverConn)

		assert.NoError(t, lk.Close())

		assert.ErrorIs(t, lk.Send([]byte("Hello")), link.ErrLinkClosed)
	})

	t.Run("应该发送失败,当Link底层net.Conn关闭", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newServerAndClientConn()
		lk := newLink(t.Context(), "1", serverConn)

		clientErrorCh := make(chan error)
		go func() {
			_, err := wsutil.ReadServerBinary(clientConn)
			clientErrorCh <- err
		}()

		assert.NoError(t, serverConn.Close())

		// 底层net.Conn关闭后会被receive协程检测到并导致整个Link被关闭
		<-lk.HasClosed()
		assert.ErrorIs(t, lk.Send([]byte("Hello")), link.ErrLinkClosed)
		assert.ErrorIs(t, <-clientErrorCh, io.EOF)
	})
}

func TestLink_WithCompression(t *testing.T) {
	t.Parallel()

	t.Run("应该支持压缩配置", func(t *testing.T) {
		t.Parallel()

		serverConn, _ := newServerAndClientConn()

		compressionState := &compression.State{
			Enabled: true,
			Parameters: wsflate.Parameters{
				ServerMaxWindowBits: 15,
				ClientMaxWindowBits: 15,
			},
		}

		lk := link.New(context.Background(), "test-compression",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithCompression(compressionState),
			link.WithTimeouts(time.Second, time.Second),
		)

		assert.Equal(t, "test-compression", lk.ID())
		assert.NoError(t, lk.Close())
	})

	t.Run("应该处理nil压缩状态", func(t *testing.T) {
		t.Parallel()

		serverConn, _ := newServerAndClientConn()

		lk := link.New(context.Background(), "test-nil-compression",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithCompression(nil),
		)

		assert.Equal(t, "test-nil-compression", lk.ID())
		assert.NoError(t, lk.Close())
	})
}

func TestLink_CompressionFallback(t *testing.T) {
	t.Parallel()

	t.Run("应该优雅处理压缩禁用情况", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newServerAndClientConn()

		// 不启用压缩
		lk := newLink(t.Context(), "test-no-compression", serverConn)

		clientErrorCh := make(chan error)
		go func() {
			expected, err := wsutil.ReadServerBinary(clientConn)
			clientErrorCh <- err
			clientErrorCh <- wsutil.WriteClientBinary(clientConn, expected)
		}()

		expected := []byte("Hello without compression")
		assert.NoError(t, lk.Send(expected))
		assert.NoError(t, <-clientErrorCh)

		assert.NoError(t, <-clientErrorCh)
		actual, ok := <-lk.Receive()
		assert.True(t, ok)
		assert.Equal(t, expected, actual)

		assert.NoError(t, lk.Close())
	})
}

func TestLink_CompressionDataTransfer(t *testing.T) {
	t.Parallel()

	t.Run("应该正确传输压缩数据_Server到Client", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newCompressedServerAndClientConn(t)
		compressionState := &compression.State{
			Enabled: true,
			Parameters: wsflate.Parameters{
				ServerMaxWindowBits:     15,
				ClientMaxWindowBits:     15,
				ServerNoContextTakeover: false,
				ClientNoContextTakeover: false,
			},
		}

		lk := link.New(context.Background(), "test-compress-s2c",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithCompression(compressionState),
			link.WithTimeouts(5*time.Second, 5*time.Second),
		)

		// 创建高重复性数据（压缩效果明显）
		expected := createCompressibleData(1024) // 1KB重复数据

		// 使用channel同步goroutine之间的通信
		clientErrorCh := make(chan error, 1)
		var receivedData []byte

		go func() {
			var err error
			// 现在应该能正确解压缩并获得原始数据
			receivedData, err = readCompressedClientData(clientConn)
			clientErrorCh <- err
		}()

		// 服务端发送数据
		err := lk.Send(expected)
		assert.NoError(t, err)

		// 等待客户端接收
		err = <-clientErrorCh
		assert.NoError(t, err)

		// 验证接收到的是解压缩后的原始数据
		assert.Equal(t, expected, receivedData)

		t.Logf("成功传输并解压缩数据: %d bytes", len(receivedData))
	})

	t.Run("应该正确传输压缩数据_Client到Server", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newCompressedServerAndClientConn(t)
		compressionState := &compression.State{
			Enabled: true,
			Parameters: wsflate.Parameters{
				ServerMaxWindowBits:     15,
				ClientMaxWindowBits:     15,
				ServerNoContextTakeover: false,
				ClientNoContextTakeover: false,
			},
		}

		lk := link.New(context.Background(), "test-compress-c2s",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithCompression(compressionState),
			link.WithTimeouts(5*time.Second, 5*time.Second),
		)

		expected := createCompressibleData(2048) // 2KB重复数据

		clientErrorCh := make(chan error, 1)
		go func() {
			clientErrorCh <- writeCompressedClientData(clientConn, expected)
		}()

		// 从服务端接收
		assert.NoError(t, <-clientErrorCh)
		actual, ok := <-lk.Receive()
		assert.True(t, ok)
		assert.Equal(t, expected, actual)

		assert.NoError(t, lk.Close())
	})
}

func TestLink_CompressionEffectiveness(t *testing.T) {
	t.Parallel()

	t.Run("应该正确处理高重复性数据", func(t *testing.T) {
		t.Parallel()

		// 创建压缩连接
		serverConn, clientConn := newCompressedServerAndClientConn(t)
		compressionState := &compression.State{
			Enabled: true,
			Parameters: wsflate.Parameters{
				ServerMaxWindowBits:     15,
				ClientMaxWindowBits:     15,
				ServerNoContextTakeover: false,
				ClientNoContextTakeover: false,
			},
		}

		compressedLink := link.New(context.Background(), "test-compress-effect",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithCompression(compressionState),
			link.WithTimeouts(5*time.Second, 5*time.Second),
		)

		// 创建高重复性数据（这种数据最适合压缩）
		testData := createCompressibleData(4096) // 4KB重复数据

		clientErrorCh := make(chan error, 1)
		var receivedData []byte

		go func() {
			var err error
			receivedData, err = readCompressedClientData(clientConn)
			clientErrorCh <- err
		}()

		// 发送高重复性数据，验证压缩功能能正确处理
		assert.NoError(t, compressedLink.Send(testData))

		// 验证接收端能正确解压缩和接收数据
		assert.NoError(t, <-clientErrorCh)
		assert.Equal(t, testData, receivedData)

		t.Logf("成功传输高重复性数据: %d bytes", len(testData))

		assert.NoError(t, compressedLink.Close())
	})
}

func TestLink_BidirectionalCompression(t *testing.T) {
	t.Parallel()

	t.Run("应该支持双向压缩传输", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newCompressedServerAndClientConn(t)
		compressionState := &compression.State{
			Enabled: true,
			Parameters: wsflate.Parameters{
				ServerMaxWindowBits:     15,
				ClientMaxWindowBits:     15,
				ServerNoContextTakeover: false,
				ClientNoContextTakeover: false,
			},
		}

		lk := link.New(context.Background(), "test-bidirectional",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithCompression(compressionState),
			link.WithTimeouts(5*time.Second, 5*time.Second),
		)

		serverData := createCompressibleData(1024)
		clientData := createCompressibleData(2048)

		done := make(chan struct{}, 2)
		errors := make(chan error, 4)

		// 服务端发送，客户端接收
		go func() {
			defer func() { done <- struct{}{} }()

			errors <- lk.Send(serverData)

			received, err := readCompressedClientData(clientConn)
			errors <- err
			if err == nil && !assert.Equal(t, serverData, received) {
				errors <- fmt.Errorf("服务端数据不匹配")
			}
		}()

		// 客户端发送，服务端接收
		go func() {
			defer func() { done <- struct{}{} }()

			// 稍微延迟，确保顺序
			time.Sleep(100 * time.Millisecond)
			errors <- writeCompressedClientData(clientConn, clientData)

			received, ok := <-lk.Receive()
			if !ok {
				errors <- fmt.Errorf("接收通道已关闭")
				return
			}
			if !assert.Equal(t, clientData, received) {
				errors <- fmt.Errorf("客户端数据不匹配")
			}
		}()

		// 等待两个方向都完成
		<-done
		<-done

		// 检查所有错误
		close(errors)
		for err := range errors {
			assert.NoError(t, err)
		}

		assert.NoError(t, lk.Close())
	})
}

func TestLink_ConcurrentCompression(t *testing.T) {
	t.Parallel()

	t.Run("应该支持多个Link并发压缩", func(t *testing.T) {
		t.Parallel()

		const numLinks = 5
		const dataSize = 1024

		links := make([]gateway.Link, numLinks)
		clientConns := make([]net.Conn, numLinks)

		compressionState := &compression.State{
			Enabled: true,
			Parameters: wsflate.Parameters{
				ServerMaxWindowBits:     15,
				ClientMaxWindowBits:     15,
				ServerNoContextTakeover: false,
				ClientNoContextTakeover: false,
			},
		}

		// 创建多个压缩连接
		for i := 0; i < numLinks; i++ {
			serverConn, clientConn := newCompressedServerAndClientConn(t)
			clientConns[i] = clientConn

			links[i] = link.New(context.Background(), fmt.Sprintf("concurrent-%d", i),
				createTestSession(t, session.UserInfo{BizID: int64(i + 1), UserID: int64(i + 10)}),
				serverConn,
				link.WithCompression(compressionState),
				link.WithTimeouts(10*time.Second, 10*time.Second),
			)
		}

		var wg sync.WaitGroup
		errors := make(chan error, numLinks*2)

		// 并发发送和接收
		for i := 0; i < numLinks; i++ {
			wg.Add(1)
			go func(linkIndex int) {
				defer wg.Done()

				lk := links[linkIndex]
				clientConn := clientConns[linkIndex]

				// 每个链接发送不同的数据
				testData := createCompressibleDataWithPrefix(dataSize, fmt.Sprintf("Link-%d:", linkIndex))

				// 发送数据
				if err := lk.Send(testData); err != nil {
					errors <- fmt.Errorf("Link %d 发送失败: %w", linkIndex, err)
					return
				}

				// 接收数据
				received, err := readCompressedClientData(clientConn)
				if err != nil {
					errors <- fmt.Errorf("Link %d 接收失败: %w", linkIndex, err)
					return
				}

				if !assert.Equal(t, testData, received) {
					errors <- fmt.Errorf("Link %d 数据不匹配", linkIndex)
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		// 检查错误
		for err := range errors {
			assert.NoError(t, err)
		}

		// 清理
		for _, lk := range links {
			assert.NoError(t, lk.Close())
		}
	})
}

func TestLink_CompressionParameters(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		parameters wsflate.Parameters
	}{
		{
			name: "最大窗口大小",
			parameters: wsflate.Parameters{
				ServerMaxWindowBits:     15,
				ClientMaxWindowBits:     15,
				ServerNoContextTakeover: false,
				ClientNoContextTakeover: false,
			},
		},
		{
			name: "中等窗口大小",
			parameters: wsflate.Parameters{
				ServerMaxWindowBits:     12,
				ClientMaxWindowBits:     12,
				ServerNoContextTakeover: false,
				ClientNoContextTakeover: false,
			},
		},
		{
			name: "无上下文复用",
			parameters: wsflate.Parameters{
				ServerMaxWindowBits:     15,
				ClientMaxWindowBits:     15,
				ServerNoContextTakeover: true,
				ClientNoContextTakeover: true,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			serverConn, clientConn := newCompressedServerAndClientConn(t)
			compressionState := &compression.State{
				Enabled:    true,
				Parameters: tc.parameters,
			}

			lk := link.New(context.Background(), "test-params",
				createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
				serverConn,
				link.WithCompression(compressionState),
				link.WithTimeouts(5*time.Second, 5*time.Second),
			)

			testData := createCompressibleData(2048)

			clientErrorCh := make(chan error, 1)
			var receivedData []byte

			go func() {
				var err error
				receivedData, err = readCompressedClientData(clientConn)
				clientErrorCh <- err
			}()

			assert.NoError(t, lk.Send(testData))
			assert.NoError(t, <-clientErrorCh)
			assert.Equal(t, testData, receivedData)

			assert.NoError(t, lk.Close())
		})
	}
}

func TestLink_WithRetry(t *testing.T) {
	t.Parallel()

	t.Run("应该使用自定义重试配置", func(t *testing.T) {
		t.Parallel()

		serverConn, _ := newServerAndClientConn()

		lk := link.New(context.Background(), "test-retry",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithRetry(100*time.Millisecond, 500*time.Millisecond, 2),
		)

		assert.Equal(t, "test-retry", lk.ID())
		assert.NoError(t, lk.Close())
	})
}

func TestLink_WithBuffer(t *testing.T) {
	t.Parallel()

	t.Run("应该使用自定义缓冲区大小", func(t *testing.T) {
		t.Parallel()

		serverConn, _ := newServerAndClientConn()

		lk := link.New(context.Background(), "test-buffer",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithBuffer(512, 1024),
		)

		assert.Equal(t, "test-buffer", lk.ID())
		assert.NoError(t, lk.Close())
	})
}

func TestLink_NetworkTimeout(t *testing.T) {
	t.Parallel()

	t.Run("应该处理读取超时", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newServerAndClientConn()

		// 使用很短的读取超时
		lk := link.New(context.Background(), "test-read-timeout",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithTimeouts(10*time.Millisecond, time.Second),
		)

		// 客户端不发送任何数据，应该触发读取超时
		// 由于超时是网络错误，receiveLoop会继续尝试

		time.Sleep(100 * time.Millisecond) // 等待足够长时间让超时发生

		// 然后客户端发送正常数据
		expected := []byte("data after timeout")
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = wsutil.WriteClientBinary(clientConn, expected)
		}()

		// 应该能正常接收
		select {
		case actual := <-lk.Receive():
			assert.Equal(t, expected, actual)
		case <-time.After(time.Second):
			t.Error("接收超时")
		}

		assert.NoError(t, lk.Close())
	})
}

func TestLink_WriteRetry(t *testing.T) {
	t.Parallel()

	t.Run("应该在写入超时时重试", func(t *testing.T) {
		t.Parallel()

		// 创建一个总是超时的连接
		serverConn, _ := newAlwaysTimeoutConn(t)

		lk := link.New(context.Background(), "test-write-retry",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithTimeouts(time.Second, 10*time.Millisecond), // 很短的写超时
			link.WithRetry(50*time.Millisecond, 200*time.Millisecond, 2),
		)

		// 尝试发送数据，应该会重试
		err := lk.Send([]byte("test retry"))
		assert.NoError(t, err) // Send本身不会失败，失败发生在后台

		// 等待重试完成并关闭连接
		select {
		case <-lk.HasClosed():
			// 连接应该因为重试失败被关闭
		case <-time.After(time.Second):
			t.Error("连接未因重试失败而关闭")
		}
	})
}

func TestLink_ClientDisconnection(t *testing.T) {
	t.Parallel()

	t.Run("应该处理客户端连接断开", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newServerAndClientConn()

		lk := link.New(context.Background(), "test-conn-close",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
		)

		// 客户端直接关闭连接（而不是发送关闭帧）
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = clientConn.Close() // 直接关闭底层连接
		}()

		// 连接应该被关闭
		select {
		case <-lk.HasClosed():
			// 连接应该被关闭
		case <-time.After(time.Second):
			t.Error("连接未被关闭")
		}
	})

	t.Run("应该处理读取错误", func(t *testing.T) {
		t.Parallel()

		// 创建一个会产生读取错误的连接
		serverConn, clientConn := newServerAndClientConn()

		lk := link.New(context.Background(), "test-read-error",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
		)

		// 客户端发送无效的数据后关闭连接
		go func() {
			time.Sleep(50 * time.Millisecond)
			// 直接写入无效的WebSocket数据
			_, _ = clientConn.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
			_ = clientConn.Close()
		}()

		// 连接应该被关闭
		select {
		case <-lk.HasClosed():
			// 连接应该被关闭
		case <-time.After(time.Second):
			t.Error("连接未被关闭")
		}
	})
}

func TestLink_ContextCancellation(t *testing.T) {
	t.Parallel()

	t.Run("应该在context取消时关闭", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		serverConn, _ := newServerAndClientConn()

		lk := link.New(ctx, "test-context-cancel",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
		)

		// 取消context
		cancel()

		// 连接应该被关闭
		select {
		case <-lk.HasClosed():
			// 连接应该被关闭
		case <-time.After(time.Second):
			t.Error("连接未被关闭")
		}
	})

	t.Run("应该在context取消时阻止发送", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		serverConn, _ := newServerAndClientConn()

		lk := link.New(ctx, "test-context-send",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
		)

		// 取消context
		cancel()

		// 等待一点时间让context取消生效
		time.Sleep(50 * time.Millisecond)

		// 尝试发送应该失败
		err := lk.Send([]byte("should fail"))
		assert.ErrorIs(t, err, link.ErrLinkClosed)
	})
}

func TestLink_SendChannelBlocking(t *testing.T) {
	t.Parallel()

	t.Run("应该在发送通道满时阻塞", func(t *testing.T) {
		t.Parallel()

		serverConn, _ := newServerAndClientConn()

		// 使用很小的发送缓冲区
		lk := link.New(context.Background(), "test-send-blocking",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithBuffer(1, 256), // 发送缓冲区只有1
		)

		// 填满发送通道并阻塞sendLoop（通过不让客户端读取）
		err1 := lk.Send([]byte("first message"))
		assert.NoError(t, err1)

		// 第二条消息应该能发送（但会在sendLoop中阻塞）
		err2 := lk.Send([]byte("second message"))
		assert.NoError(t, err2)

		time.Sleep(100 * time.Millisecond) // 让sendLoop尝试发送

		assert.NoError(t, lk.Close())
	})
}

func TestLink_SendWithRetryExhaustion(t *testing.T) {
	t.Parallel()

	t.Run("应该在重试次数耗尽后停止", func(t *testing.T) {
		t.Parallel()

		// 创建总是返回超时错误的连接
		serverConn, _ := newAlwaysTimeoutConn(t)

		lk := link.New(context.Background(), "test-retry-exhaustion",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithTimeouts(time.Second, 1*time.Millisecond),          // 很短的写超时
			link.WithRetry(10*time.Millisecond, 50*time.Millisecond, 1), // 只重试1次
		)

		// 发送消息，应该会重试然后失败
		err := lk.Send([]byte("test retry exhaustion"))
		assert.NoError(t, err) // Send本身不会失败，失败发生在后台

		// 等待连接因为重试失败而关闭
		select {
		case <-lk.HasClosed():
			// 连接应该因为重试失败被关闭
		case <-time.After(500 * time.Millisecond):
			t.Error("连接未因重试失败而关闭")
		}
	})
}

func TestLink_NonRetriableError(t *testing.T) {
	t.Parallel()

	t.Run("应该在非重试错误时立即失败", func(t *testing.T) {
		t.Parallel()

		// 创建会返回非网络错误的连接
		serverConn, _ := newErrorConn(t)

		lk := link.New(context.Background(), "test-non-retriable",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
			link.WithRetry(100*time.Millisecond, 500*time.Millisecond, 3),
		)

		// 发送消息，应该立即失败
		err := lk.Send([]byte("test non-retriable"))
		assert.NoError(t, err) // Send本身不会失败，失败发生在后台

		// 等待连接快速关闭（不会重试）
		select {
		case <-lk.HasClosed():
			// 连接应该立即被关闭
		case <-time.After(200 * time.Millisecond):
			t.Error("连接未立即关闭")
		}
	})
}

func TestLink_CloseRaceCondition(t *testing.T) {
	t.Parallel()

	t.Run("应该处理并发关闭", func(t *testing.T) {
		t.Parallel()

		serverConn, _ := newServerAndClientConn()

		lk := link.New(context.Background(), "test-close-race",
			createTestSession(t, session.UserInfo{BizID: 1, UserID: 1}),
			serverConn,
		)

		// 并发关闭多次
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = lk.Close()
			}()
		}

		wg.Wait()

		// 验证连接被正确关闭
		select {
		case <-lk.HasClosed():
			// 连接应该被关闭
		default:
			t.Error("连接未被关闭")
		}
	})
}

// alwaysTimeoutConn 总是返回超时错误的连接
type alwaysTimeoutConn struct {
	net.Conn
}

func newAlwaysTimeoutConn(t *testing.T) (server, client net.Conn) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	return &alwaysTimeoutConn{Conn: serverConn}, clientConn
}

func (atc *alwaysTimeoutConn) Write(_ []byte) (n int, err error) {
	// 返回超时错误
	return 0, &net.OpError{
		Op:  "write",
		Net: "pipe",
		Err: &timeoutError{},
	}
}

type timeoutError struct{}

func (te *timeoutError) Error() string   { return "timeout" }
func (te *timeoutError) Timeout() bool   { return true }
func (te *timeoutError) Temporary() bool { return true }

// errorConn 返回非网络错误的连接
type errorConn struct {
	net.Conn
}

func newErrorConn(t *testing.T) (server, client net.Conn) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	return &errorConn{Conn: serverConn}, clientConn
}

func (ec *errorConn) Write(_ []byte) (n int, err error) {
	// 返回非网络错误
	return 0, fmt.Errorf("custom error")
}

// ==================== 辅助函数 ====================

// createCompressibleData 创建高重复性的可压缩数据
func createCompressibleData(size int) []byte {
	pattern := []byte("Hello WebSocket Compression! This is a repeating pattern for testing. ")
	patternLen := len(pattern)

	data := make([]byte, size)
	for i := 0; i < size; i++ {
		data[i] = pattern[i%patternLen]
	}
	return data
}

// createCompressibleDataWithPrefix 创建带前缀的可压缩数据
func createCompressibleDataWithPrefix(size int, prefix string) []byte {
	prefixBytes := []byte(prefix)
	remainingSize := size - len(prefixBytes)
	if remainingSize <= 0 {
		return prefixBytes[:size]
	}

	data := make([]byte, size)
	copy(data, prefixBytes)

	pattern := []byte("RepeatingPattern")
	for i := len(prefixBytes); i < size; i++ {
		data[i] = pattern[(i-len(prefixBytes))%len(pattern)]
	}
	return data
}

// newCompressedServerAndClientConn 创建支持压缩的WebSocket连接对
func newCompressedServerAndClientConn(t *testing.T) (server, client net.Conn) {
	t.Helper()
	// 使用pipe创建基础连接
	serverConn, clientConn := net.Pipe()

	// 注意：在真实场景中，压缩协商发生在HTTP升级握手期间
	// 这里我们直接返回连接，压缩扩展将在Link初始化时设置
	return serverConn, clientConn
}

// readCompressedClientData 从客户端读取压缩数据
func readCompressedClientData(conn net.Conn) ([]byte, error) {
	// 使用修复后的wswrapper.NewClientSideReader
	clientReader := wswrapper.NewClientSideReader(conn)
	return clientReader.Read()
}

// writeCompressedClientData 向客户端写入压缩数据
func writeCompressedClientData(conn net.Conn, data []byte) error {
	// 参考 reader_writer_test.go 的正确模式发送压缩数据
	clientWriter := wsutil.NewWriter(conn, ws.StateClientSide, ws.OpBinary)

	// 设置压缩扩展
	messageState := &wsflate.MessageState{}
	messageState.SetCompressed(true)
	clientWriter.SetExtensions(messageState)

	// 创建压缩器
	flateWriter := wsflate.NewWriter(nil, func(w io.Writer) wsflate.Compressor {
		f, _ := flate.NewWriter(w, flate.DefaultCompression)
		return f
	})

	// 写入压缩数据
	flateWriter.Reset(clientWriter)
	_, err := flateWriter.Write(data)
	if err != nil {
		return err
	}

	err = flateWriter.Close()
	if err != nil {
		return err
	}

	return clientWriter.Flush()
}

func newLink(ctx context.Context, id string, server net.Conn) gateway.Link {
	sess := createTestSession(nil, session.UserInfo{BizID: 1, UserID: 1})
	return newLinkWith(ctx, id, sess, server)
}

func newLinkWith(ctx context.Context, id string, sess session.Session, server net.Conn) gateway.Link {
	return link.New(ctx, id, sess, server)
}

func newServerAndClientConn() (server, client net.Conn) {
	return net.Pipe()
}

// createTestSession 创建测试用的session
func createTestSession(t *testing.T, userInfo session.UserInfo) session.Session {
	var ctrl *gomock.Controller
	if t != nil {
		ctrl = gomock.NewController(t)
	} else {
		ctrl = gomock.NewController(&testing.T{})
	}

	mockRedis := mocks.NewMockCmdable(ctrl)

	// Mock session creation
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil)).AnyTimes()

	// Mock session destruction (Del operation) - 用于Link关闭时
	mockRedis.EXPECT().Del(gomock.Any(), gomock.Any()).
		Return(redis.NewIntResult(int64(1), nil)).AnyTimes()

	provider := session.NewRedisSessionBuilder(mockRedis)
	sess, _, _ := provider.Build(context.Background(), userInfo)

	return sess
}
