package wswrapper

import (
	"compress/flate"
	"io"
	"net"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gobwas/ws/wsutil"
)

type Reader struct {
	conn           net.Conn
	reader         *wsutil.Reader
	controlHandler wsutil.FrameHandlerFunc
	messageState   *wsflate.MessageState
	flateReader    *wsflate.Reader
}

func NewReader(conn net.Conn) *Reader {
	messageState := &wsflate.MessageState{}
	controlHandler := wsutil.ControlFrameHandler(conn, ws.StateServerSide)
	return &Reader{
		conn: conn,
		reader: &wsutil.Reader{
			Source:         conn,
			State:          ws.StateServerSide | ws.StateExtended,
			Extensions:     []wsutil.RecvExtension{messageState},
			OnIntermediate: controlHandler,
		},
		controlHandler: controlHandler,
		messageState:   messageState,
		flateReader: wsflate.NewReader(nil, func(r io.Reader) wsflate.Decompressor {
			return flate.NewReader(r) // 标准库实现
		}),
	}
}

func (r *Reader) Read() (payload []byte, err error) {
	for {
		header, err1 := r.reader.NextFrame()
		if err1 != nil {
			return nil, err1
		}

		if header.OpCode.IsControl() {
			if err2 := r.controlHandler(header, r.reader); err2 != nil {
				return nil, err2
			}
			continue
		}

		if r.messageState.IsCompressed() {
			r.flateReader.Reset(r.reader)
			return io.ReadAll(r.flateReader)
		}
		return io.ReadAll(r.reader)
	}
}
