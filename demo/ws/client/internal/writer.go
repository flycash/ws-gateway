package internal

import (
	"compress/flate"
	"io"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gobwas/ws/wsutil"
)

// ClientWriter WebSocket客户端写入器，支持压缩
type ClientWriter struct {
	writer       *wsutil.Writer
	messageState *wsflate.MessageState
	flateWriter  *wsflate.Writer
}

// NewClientWriter 创建新的客户端写入器
// dest: 目标连接
// compressed: 是否启用压缩
func NewClientWriter(dest io.Writer, compressed bool) *ClientWriter {
	messageState := wsflate.MessageState{}
	messageState.SetCompressed(compressed)
	state := ws.StateClientSide | ws.StateExtended
	opCode := ws.OpBinary
	w := &ClientWriter{
		writer:       wsutil.NewWriter(dest, state, opCode),
		messageState: &messageState,
		flateWriter: wsflate.NewWriter(nil, func(w io.Writer) wsflate.Compressor {
			f, _ := flate.NewWriter(w, flate.DefaultCompression)
			return f
		}),
	}
	w.writer.SetExtensions(&messageState)
	return w
}

// Write 写入数据，根据压缩设置自动选择压缩或非压缩方式
func (w *ClientWriter) Write(p []byte) (n int, err error) {
	if w.messageState.IsCompressed() {
		return w.writeCompressed(p)
	}
	return w.writeUncompressed(p)
}

// writeCompressed 写入压缩消息
func (w *ClientWriter) writeCompressed(p []byte) (n int, err error) {
	w.flateWriter.Reset(w.writer)

	// 写入压缩数据
	n, err = w.flateWriter.Write(p)
	if err != nil {
		return 0, err
	}

	// 完成flate writer压缩（写入尾部标记）
	err = w.flateWriter.Close()
	if err != nil {
		return 0, err
	}

	// 刷新WebSocket writer
	return n, w.writer.Flush()
}

// writeUncompressed 写入未压缩消息
func (w *ClientWriter) writeUncompressed(p []byte) (n int, err error) {
	// 写入未压缩数据
	n, err = w.writer.Write(p)
	if err != nil {
		return 0, err
	}
	// 刷新WebSocket writer
	return n, w.writer.Flush()
}

// Flush 刷新缓冲区
func (w *ClientWriter) Flush() error {
	return w.writer.Flush()
}
