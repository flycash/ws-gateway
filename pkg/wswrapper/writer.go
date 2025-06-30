package wswrapper

import (
	"compress/flate"
	"io"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gobwas/ws/wsutil"
)

type Writer struct {
	writer       *wsutil.Writer
	messageState *wsflate.MessageState
	flateWriter  *wsflate.Writer
}

func NewServerSideWriter(dest io.Writer, compressed bool) *Writer {
	messageState := wsflate.MessageState{}
	messageState.SetCompressed(compressed)
	state := ws.StateServerSide | ws.StateExtended
	opCode := ws.OpBinary
	w := &Writer{
		writer:       wsutil.NewWriter(dest, state, opCode),
		messageState: &messageState,
	}
	if compressed {
		w.flateWriter = wsflate.NewWriter(nil, func(w io.Writer) wsflate.Compressor {
			f, _ := flate.NewWriter(w, flate.DefaultCompression)
			return f
		})
	}
	w.writer.SetExtensions(&messageState)
	return w
}

func (w *Writer) Write(p []byte) (n int, err error) {
	if w.messageState.IsCompressed() {
		return w.writeCompressed(p)
	}
	return w.writeUncompressed(p)
}

// writeCompressed 写入压缩消息
func (w *Writer) writeCompressed(p []byte) (n int, err error) {
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
func (w *Writer) writeUncompressed(p []byte) (n int, err error) {
	// 写入未压缩数据
	n, err = w.writer.Write(p)
	if err != nil {
		return 0, err
	}
	// 刷新WebSocket writer
	return n, w.writer.Flush()
}
