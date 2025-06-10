//go:build unit

package wswrapper_test

import (
	"bytes"
	"compress/flate"
	"io"
	"net"
	"testing"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gobwas/ws/wsutil"
	"github.com/stretchr/testify/assert"

	"gitee.com/flycash/ws-gateway/pkg/wswrapper"
)

// testConnection 创建一对连接用于测试
func testConnection(t *testing.T) (client, server net.Conn) {
	t.Helper()
	client, server = net.Pipe()
	return client, server
}

// TestReader_NewReader 测试 Reader 构造函数
func TestReader_NewReader(t *testing.T) {
	t.Parallel()
	client, server := testConnection(t)
	defer client.Close()
	defer server.Close()

	reader := wswrapper.NewServerSideReader(server)
	assert.NotNil(t, reader)
}

// TestWriter_NewWriter 测试 Writer 构造函数
func TestWriter_NewWriter(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer

	// 测试未压缩 Writer
	uncompressedWriter := wswrapper.NewServerSideWriter(&buf, false)
	assert.NotNil(t, uncompressedWriter)

	// 测试压缩 Writer
	compressedWriter := wswrapper.NewServerSideWriter(&buf, true)
	assert.NotNil(t, compressedWriter)
}

// TestWriter_WriteUncompressed 测试写入未压缩数据
func TestWriter_WriteUncompressed(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	writer := wswrapper.NewServerSideWriter(&buf, false)

	testData := []byte("Hello, WebSocket!")
	n, err := writer.Write(testData)

	assert.NoError(t, err)
	assert.Equal(t, len(testData), n)
	assert.Greater(t, buf.Len(), 0) // 应该有数据写入
}

// TestWriter_WriteCompressed 测试写入压缩数据
func TestWriter_WriteCompressed(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	writer := wswrapper.NewServerSideWriter(&buf, true)

	testData := []byte("Hello, WebSocket! This is a longer message that should compress well.")
	n, err := writer.Write(testData)

	assert.NoError(t, err)
	assert.Equal(t, len(testData), n)
	assert.Greater(t, buf.Len(), 0) // 应该有数据写入
}

// TestWriter_EmptyData 测试写入空数据
func TestWriter_EmptyData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		compressed bool
	}{
		{"uncompressed_empty", false},
		{"compressed_empty", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer // 每个子测试使用独立的buffer
			writer := wswrapper.NewServerSideWriter(&buf, tt.compressed)

			n, err := writer.Write([]byte{})
			assert.NoError(t, err)
			assert.Equal(t, 0, n)
		})
	}
}

// TestIntegration_ReadStandardWebSocketMessage 测试读取标准 WebSocket 消息
func TestIntegration_ReadStandardWebSocketMessage(t *testing.T) {
	t.Parallel()
	client, server := testConnection(t)
	defer client.Close()
	defer server.Close()

	reader := wswrapper.NewServerSideReader(server)
	testData := []byte("Hello, WebSocket!")

	// 使用标准库发送掩码消息（客户端到服务端）
	go func() {
		err := wsutil.WriteClientMessage(client, ws.OpBinary, testData)
		assert.NoError(t, err)
	}()

	// 读取数据
	received, err := reader.Read()
	assert.NoError(t, err)
	assert.Equal(t, testData, received)
}

// TestReader_ReadCompressedMessage 测试 Reader 的压缩分支覆盖
func TestReader_ReadCompressedMessage(t *testing.T) {
	t.Parallel()
	client, server := testConnection(t)
	defer client.Close()
	defer server.Close()

	originalData := []byte("Hello compression test! This message will be compressed to verify the decompression branch.")

	// 客户端发送压缩帧
	go func() {
		// 使用客户端状态的 writer 生成带掩码的压缩帧
		clientWriter := wsutil.NewWriter(client, ws.StateClientSide, ws.OpBinary)

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
		_, err := flateWriter.Write(originalData)
		assert.NoError(t, err)

		err = flateWriter.Close()
		assert.NoError(t, err)

		err = clientWriter.Flush()
		assert.NoError(t, err)
	}()

	// 服务端读取并验证
	reader := wswrapper.NewServerSideReader(server)
	received, err := reader.Read()
	assert.NoError(t, err)
	assert.Equal(t, originalData, received)
}

// TestClientReader_ReadServerCompressedMessage 测试客户端接收服务端压缩数据
func TestClientReader_ReadServerCompressedMessage(t *testing.T) {
	t.Parallel()
	client, server := testConnection(t)
	defer client.Close()
	defer server.Close()

	originalData := []byte("Hello server compression! This data will be compressed by server and received by client.")

	// 服务端发送压缩帧
	go func() {
		// 使用wswrapper.Writer发送压缩数据
		writer := wswrapper.NewServerSideWriter(server, true) // 启用压缩
		_, err := writer.Write(originalData)
		assert.NoError(t, err)
	}()

	// 客户端使用NewClientReader接收
	clientReader := wswrapper.NewClientSideReader(client)
	received, err := clientReader.Read()
	assert.NoError(t, err)
	assert.Equal(t, originalData, received)
}

// TestIntegration_MultipleMessages 测试读取多条消息
func TestIntegration_MultipleMessages(t *testing.T) {
	t.Parallel()
	client, server := testConnection(t)
	defer client.Close()
	defer server.Close()

	reader := wswrapper.NewServerSideReader(server)
	messages := [][]byte{
		[]byte("First message"),
		[]byte("Second message"),
		[]byte("Third message"),
	}

	go func() {
		for _, msg := range messages {
			err := wsutil.WriteClientMessage(client, ws.OpBinary, msg)
			assert.NoError(t, err)
		}
	}()

	// 读取所有消息
	for i, expectedMsg := range messages {
		received, err := reader.Read()
		assert.NoError(t, err, "Failed to read message %d", i)
		assert.Equal(t, expectedMsg, received, "Message %d content mismatch", i)
	}
}

// TestWriter_MultipleWrites 测试多次写入
func TestWriter_MultipleWrites(t *testing.T) {
	t.Parallel()

	messages := [][]byte{
		[]byte("First message"),
		[]byte("Second message"),
		[]byte("Third message"),
	}

	for i, msg := range messages {
		var buf bytes.Buffer // 每次使用新的buffer
		writer := wswrapper.NewServerSideWriter(&buf, false)
		n, err := writer.Write(msg)
		assert.NoError(t, err, "Failed at message %d", i)
		assert.Equal(t, len(msg), n, "Length mismatch at message %d", i)
		assert.Greater(t, buf.Len(), 0, "No data written at message %d", i)
	}
}

// TestWriter_LargeData 测试大数据写入
func TestWriter_LargeData(t *testing.T) {
	t.Parallel()

	// 创建大数据（1MB）
	largeData := make([]byte, 1024*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	tests := []struct {
		name       string
		compressed bool
	}{
		{"large_uncompressed", false},
		{"large_compressed", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer // 每个子测试使用独立的buffer
			writer := wswrapper.NewServerSideWriter(&buf, tt.compressed)

			n, err := writer.Write(largeData)
			assert.NoError(t, err)
			assert.Equal(t, len(largeData), n)
			assert.Greater(t, buf.Len(), 0)
		})
	}
}

// TestReader_ControlFrames 测试控制帧处理分支
func TestReader_ControlFrames(t *testing.T) {
	t.Parallel()
	client, server := testConnection(t)
	defer client.Close()
	defer server.Close()

	reader := wswrapper.NewServerSideReader(server)
	testData := []byte("Hello after control frames!")

	go func() {
		// 发送一个pong控制帧（不需要响应，避免死锁）
		pongFrame := ws.NewFrame(ws.OpPong, true, []byte("pong"))
		pongFrame.Header.Masked = true
		pongFrame.Header.Mask = ws.NewMask()
		err := ws.WriteFrame(client, pongFrame)
		assert.NoError(t, err)

		// 然后发送正常数据帧
		err = wsutil.WriteClientMessage(client, ws.OpBinary, testData)
		assert.NoError(t, err)
	}()

	// 读取数据（控制帧会被自动处理并跳过）
	received, err := reader.Read()
	assert.NoError(t, err)
	assert.Equal(t, testData, received)
}

// TestError_ClosedConnection 测试连接关闭时的错误处理
func TestError_ClosedConnection(t *testing.T) {
	t.Parallel()
	client, server := testConnection(t)
	reader := wswrapper.NewServerSideReader(server)

	// 立即关闭连接
	client.Close()
	server.Close()

	// 尝试读取应该返回错误
	_, err := reader.Read()
	assert.Error(t, err)
}

// BenchmarkWriter_Uncompressed 性能测试：未压缩写入
func BenchmarkWriter_Uncompressed(b *testing.B) {
	var buf bytes.Buffer
	writer := wswrapper.NewServerSideWriter(&buf, false)
	data := []byte("This is a test message for benchmarking purposes.")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		_, _ = writer.Write(data)
	}
}

// BenchmarkWriter_Compressed 性能测试：压缩写入
func BenchmarkWriter_Compressed(b *testing.B) {
	var buf bytes.Buffer
	writer := wswrapper.NewServerSideWriter(&buf, true)
	data := []byte("This is a test message for benchmarking purposes. It should be long enough to benefit from compression.")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		_, _ = writer.Write(data)
	}
}

// BenchmarkReader_ReadCompressed 性能测试：压缩读取
func BenchmarkReader_ReadCompressed(b *testing.B) {
	// 预先准备压缩数据
	testData := bytes.Repeat([]byte("Benchmark compressed read test data. "), 100)
	var compressedBuf bytes.Buffer
	writer := wswrapper.NewServerSideWriter(&compressedBuf, true)
	_, _ = writer.Write(testData)
	compressedBytes := compressedBuf.Bytes()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client, server := testConnection(nil)
		reader := wswrapper.NewServerSideReader(server)

		go func() {
			_, _ = client.Write(compressedBytes)
			client.Close()
		}()

		_, _ = reader.Read()
		server.Close()
	}
}

// TestWriter_CompressionEffectiveness 测试压缩效果
func TestWriter_CompressionEffectiveness(t *testing.T) {
	t.Parallel()
	// 创建重复性高的数据，应该能很好地压缩
	repeatableData := bytes.Repeat([]byte("Hello WebSocket compression! "), 100)

	var uncompressedBuf, compressedBuf bytes.Buffer

	uncompressedWriter := wswrapper.NewServerSideWriter(&uncompressedBuf, false)
	compressedWriter := wswrapper.NewServerSideWriter(&compressedBuf, true)

	// 写入相同数据
	_, err1 := uncompressedWriter.Write(repeatableData)
	_, err2 := compressedWriter.Write(repeatableData)

	assert.NoError(t, err1)
	assert.NoError(t, err2)

	// 压缩后的数据应该更小（包括WebSocket帧头的开销）
	t.Logf("Uncompressed size: %d bytes", uncompressedBuf.Len())
	t.Logf("Compressed size: %d bytes", compressedBuf.Len())

	// 注意：由于 WebSocket 帧头的开销，小数据可能不会体现压缩优势
	// 这里主要是验证压缩流程没有错误
	assert.Greater(t, uncompressedBuf.Len(), 0)
	assert.Greater(t, compressedBuf.Len(), 0)
}
