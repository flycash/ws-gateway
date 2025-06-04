package link

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	gateway "gitee.com/flycash/ws-gateway"
	"github.com/gobwas/pool/pbufio"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/gotomicro/ego/core/elog"
)

var (
	_             gateway.Link = &link{}
	ErrLinkClosed              = errors.New("websocket: 连接已关闭")
)

type link struct {
	id        string
	uid       int64
	conn      net.Conn
	rd        *bufio.Reader
	wt        *bufio.Writer
	sendCh    chan []byte
	receiveCh chan []byte

	ctx           context.Context
	ctxCancelFunc context.CancelFunc
	closeOnce     sync.Once
	logger        *elog.Component
}

func New(id string, uid int64, conn net.Conn) gateway.Link {
	ctx, cancelFunc := context.WithCancel(context.Background())
	l := &link{
		id:            id,
		uid:           uid,
		conn:          conn,
		rd:            pbufio.GetReader(conn, ws.DefaultServerReadBufferSize),
		wt:            pbufio.GetWriter(conn, ws.DefaultServerWriteBufferSize),
		sendCh:        make(chan []byte),
		receiveCh:     make(chan []byte),
		ctx:           ctx,
		ctxCancelFunc: cancelFunc,
		logger:        elog.EgoLogger.With(elog.FieldComponent("Link")),
	}
	go l.send()
	go l.receive()
	return l
}

func (l *link) send() {
	for {
		select {
		case <-l.ctx.Done():
			return
		case payload, ok := <-l.sendCh:
			if !ok {
				return
			}
			err := wsutil.WriteServerBinary(l.conn, payload)
			if err != nil {
				l.logger.Error("向客户端发消息失败",
					elog.String("payload", string(payload)),
					elog.FieldErr(err),
				)
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
					_ = l.Close()
					return
				}
				// todo: 超时重试逻辑
				continue
			}
		}
	}
}

func (l *link) receive() {
	defer func() {
		close(l.receiveCh)
	}()
	for {
		// 不要修改 这里前端难以控制 后续统一为二进制
		// payload, _, err := wsutil.ReadClientData(l.conn)
		payload, err := wsutil.ReadClientBinary(l.conn)
		if err != nil {
			l.logger.Error("从客户端读取消息失败",
				elog.Any("linkID", l.id),
				elog.Any("userID", l.uid),
				elog.FieldErr(err),
			)
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				// 客户端连接断开
				_ = l.Close()
				return
			}
			continue
		}
		select {
		case <-l.ctx.Done():
			return
		case l.receiveCh <- payload:
		}
	}
}

func (l *link) ID() string {
	return l.id
}

func (l *link) UID() int64 {
	return l.uid
}

func (l *link) Send(payload []byte) error {
	select {
	case <-l.ctx.Done():
		close(l.sendCh)
		return fmt.Errorf("%w", ErrLinkClosed)
	case l.sendCh <- payload:
		return nil
	}
}

func (l *link) Receive() <-chan []byte {
	return l.receiveCh
}

func (l *link) Close() error {
	l.closeOnce.Do(func() {
		pbufio.PutReader(l.rd)
		pbufio.PutWriter(l.wt)
		l.ctxCancelFunc()
	})
	return l.conn.Close()
}

func (l *link) HasClosed() <-chan struct{} {
	return l.ctx.Done()
}
