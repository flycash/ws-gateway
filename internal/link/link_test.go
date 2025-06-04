//go:build unit

package link_test

import (
	"errors"
	"io"
	"net"
	"testing"

	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/internal/link"
	"github.com/gobwas/ws/wsutil"
	"github.com/stretchr/testify/assert"
)

func TestLink_New_ID_UID(t *testing.T) {
	t.Parallel()

	server, _ := newServerAndClientConn()
	id := "1"
	uid := int64(2)
	lk := newLinkWith(id, uid, server)
	assert.Equal(t, id, lk.ID())
	assert.Equal(t, uid, lk.UID())
}

func TestLink_Close(t *testing.T) {
	t.Parallel()

	t.Run("应该是开启的,当Link刚刚被创建", func(t *testing.T) {
		t.Parallel()

		serverConn, _ := newServerAndClientConn()
		lk := newLink("2", serverConn)

		select {
		case <-lk.HasClosed():
			assert.NoError(t, errors.New("刚刚创建的link不应该是被关闭的"))
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
		lk := newLink("1", serverConn)

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
		lk := newLink("2", serverConn)

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
		lk := newLink("2", serverConn)

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
		lk := newLink("1", serverConn)

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
		lk := newLink("1", serverConn)

		assert.NoError(t, lk.Close())

		assert.ErrorIs(t, lk.Send([]byte("Hello")), link.ErrLinkClosed)
	})

	t.Run("应该发送失败,当Link底层net.Conn关闭", func(t *testing.T) {
		t.Parallel()

		serverConn, clientConn := newServerAndClientConn()
		lk := newLink("1", serverConn)

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

func newLink(id string, server net.Conn) gateway.Link {
	return newLinkWith(id, 123, server)
}

func newLinkWith(id string, uid int64, server net.Conn) gateway.Link {
	return link.New(id, uid, server)
}

func newServerAndClientConn() (server, client net.Conn) {
	return net.Pipe()
}
