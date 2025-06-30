//go:build unit

package prof_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"gitee.com/flycash/ws-gateway/demo/prof"
	"github.com/ecodeclub/ekit/pool"
	"github.com/stretchr/testify/require"
)

// -------------------- mock TaskPool --------------------

type mockTaskPool struct {
	wg   sync.WaitGroup
	mu   sync.RWMutex
	done bool
}

func (m *mockTaskPool) Submit(ctx context.Context, task pool.Task) error {
	m.mu.RLock()
	if m.done {
		m.mu.RUnlock()
		return errors.New("task pool is shut down")
	}
	m.wg.Add(1)
	m.mu.RUnlock()

	go func() {
		defer m.wg.Done()
		_ = task.Run(ctx)
	}()
	return nil
}

func (m *mockTaskPool) Start() error { return nil }

func (m *mockTaskPool) Shutdown() (<-chan struct{}, error) {
	m.mu.Lock()
	m.done = true
	m.mu.Unlock()

	ch := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(ch)
	}()
	return ch, nil
}

func (m *mockTaskPool) ShutdownNow() ([]pool.Task, error) { return nil, nil }

func (m *mockTaskPool) States(_ context.Context, _ time.Duration) (<-chan pool.State, error) {
	ch := make(chan pool.State)
	close(ch)
	return ch, nil
}

// Wait 用于测试结束时等待所有任务完成
func (m *mockTaskPool) Wait() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	m.wg.Wait()
}

// failingTaskPool 用于测试 Submit 失败的情况
type failingTaskPool struct{}

func (f *failingTaskPool) Submit(_ context.Context, _ pool.Task) error {
	return errors.New("submit failed")
}

func (f *failingTaskPool) Start() error { return nil }
func (f *failingTaskPool) Shutdown() (<-chan struct{}, error) {
	ch := make(chan struct{})
	close(ch)
	return ch, nil
}
func (f *failingTaskPool) ShutdownNow() ([]pool.Task, error) { return nil, nil }
func (f *failingTaskPool) States(_ context.Context, _ time.Duration) (<-chan pool.State, error) {
	ch := make(chan pool.State)
	close(ch)
	return ch, nil
}

// -------------------------------------------------------

func TestPartLinkWriteGroup_MultipleLinks(t *testing.T) {
	t.Parallel()
	const linkCnt = 5

	group := prof.NewPartLinkWriteGroup(t.Context(), 32)
	defer group.Close()

	var wg sync.WaitGroup
	connections := make([]struct {
		client net.Conn
		server net.Conn
	}, 0, linkCnt)

	// 清理连接
	defer func() {
		for _, conn := range connections {
			conn.client.Close()
			conn.server.Close()
		}
	}()

	// 创建多个 Link 并进行读写测试
	for i := range linkCnt {
		client, server := net.Pipe()
		connections = append(connections, struct {
			client net.Conn
			server net.Conn
		}{client, server})

		id := fmt.Sprintf("link_%d", i)
		link := prof.NewLink(id, server)
		group.Add(link)

		// 为每个 link 启动读协程
		wg.Add(1)
		go func(idx int, c net.Conn, _ string) {
			defer wg.Done()
			expected := fmt.Appendf(nil, "message_from_part_group_%d", idx)
			buf := make([]byte, len(expected))
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			n, err := io.ReadFull(c, buf)
			require.NoError(t, err, "链接 %d 应该读取成功", idx)
			require.Equal(t, len(expected), n, "链接 %d 应该读取正确长度", idx)
			require.Equal(t, expected, buf, "链接 %d 应该读取正确内容", idx)
		}(i, client, id)

		// 通过 WriteGroup 发送消息
		msg := fmt.Appendf(nil, "message_from_part_group_%d", i)
		err := group.Write(id, msg)
		require.NoError(t, err, "写入链接 %d 应该成功", i)
	}

	// 等待所有读操作完成
	wg.Wait()

	// 测试删除 link 后的行为
	lastLink := prof.NewLink("to_be_deleted", connections[0].server)
	group.Add(lastLink)
	group.Del(lastLink)

	// 对已删除的 link 写入应该触发警告日志，但不会报错
	err := group.Write("to_be_deleted", []byte("should_be_ignored"))
	require.NoError(t, err)

	// 测试关闭后的行为
	group.Close()
	time.Sleep(10 * time.Millisecond) // 确保 Close 生效
	err = group.Write("link_0", []byte("after_close"))
	require.Error(t, err)
	require.Equal(t, prof.ErrLinkGroupClosed, err)
}

func TestPartLinkWriteGroup_UnbufferedChannel(t *testing.T) {
	t.Parallel()
	// 测试无缓冲通道的情况
	group := prof.NewPartLinkWriteGroup(t.Context(), 0)
	defer group.Close()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	link := prof.NewLink("unbuffered_test", server)
	group.Add(link)

	var wg sync.WaitGroup

	// 启动读协程
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 5)
		_ = client.SetReadDeadline(time.Now().Add(time.Second))
		n, err := client.Read(buf)
		require.NoError(t, err)
		require.Equal(t, "hello", string(buf[:n]))
	}()

	// 稍微延迟后写入，确保读协程已准备好
	time.Sleep(10 * time.Millisecond)
	err := group.Write("unbuffered_test", []byte("hello"))
	require.NoError(t, err)

	wg.Wait()

	// 测试 Link 的 Read 方法 - 从 client 向 server 写入数据
	go func() {
		time.Sleep(10 * time.Millisecond)
		_, _ = client.Write([]byte("world"))
	}()

	readBuf := make([]byte, 5)
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	n, err := link.Read(readBuf)
	require.NoError(t, err)
	require.Equal(t, "world", string(readBuf[:n]))
}

func TestAllLinkWriteGroup_MultipleLinks(t *testing.T) {
	t.Parallel()
	const linkCnt = 5

	tp := &mockTaskPool{}
	group, err := prof.NewAllLinkWriteGroup(t.Context(), 32, tp)
	require.NoError(t, err)
	defer group.Close()

	var wg sync.WaitGroup
	connections := make([]struct {
		client net.Conn
		server net.Conn
	}, 0, linkCnt)

	// 清理连接
	defer func() {
		for _, conn := range connections {
			conn.client.Close()
			conn.server.Close()
		}
	}()

	// 创建多个 Link 并进行读写测试
	for i := range linkCnt {
		client, server := net.Pipe()
		connections = append(connections, struct {
			client net.Conn
			server net.Conn
		}{client, server})

		id := fmt.Sprintf("all_link_%d", i)
		link := prof.NewLink(id, server)
		group.Add(link)

		// 为每个 link 启动读协程
		wg.Add(1)
		go func(idx int, c net.Conn, _ string) {
			defer wg.Done()
			expected := fmt.Appendf(nil, "message_from_all_group_%d", idx)
			buf := make([]byte, len(expected))
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			n, err := io.ReadFull(c, buf)
			require.NoError(t, err, "链接 %d 应该读取成功", idx)
			require.Equal(t, len(expected), n, "链接 %d 应该读取正确长度", idx)
			require.Equal(t, expected, buf, "链接 %d 应该读取正确内容", idx)
		}(i, client, id)

		// 通过 WriteGroup 发送消息
		msg := fmt.Appendf(nil, "message_from_all_group_%d", i)
		err := group.Write(id, msg)
		require.NoError(t, err, "写入链接 %d 应该成功", i)
	}

	// 等待任务池调度完所有异步写
	tp.Wait()

	// 等待所有读操作完成
	wg.Wait()

	// 测试删除 link 后的行为
	lastLink := prof.NewLink("to_be_deleted_all", connections[0].server)
	group.Add(lastLink)
	group.Del(lastLink)

	// 对已删除的 link 写入应该触发警告日志，但不会报错
	err = group.Write("to_be_deleted_all", []byte("should_be_ignored"))
	require.NoError(t, err)

	// 测试关闭后的行为
	group.Close()
	time.Sleep(10 * time.Millisecond) // 确保 Close 生效
	err = group.Write("all_link_0", []byte("after_close"))
	require.Error(t, err)
	require.Equal(t, prof.ErrLinkGroupClosed, err)
}

func TestAllLinkWriteGroup_EdgeCases(t *testing.T) {
	t.Parallel()
	// 测试 nil taskPool
	_, err := prof.NewAllLinkWriteGroup(t.Context(), 16, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "taskPool 不能为空")

	// 测试 taskPool Submit 失败的情况
	failingTP := &failingTaskPool{}
	group, err := prof.NewAllLinkWriteGroup(t.Context(), 16, failingTP)
	require.NoError(t, err)
	defer group.Close()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	link := prof.NewLink("fail_test", server)
	group.Add(link)

	// 这应该触发 Submit 失败的日志，但 Write 本身不会报错
	err = group.Write("fail_test", []byte("test"))
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond) // 等待 writeLoop 处理
}

func TestLink_ReadWrite(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	link := prof.NewLink("test_link", server)

	// 测试 ID 方法
	require.Equal(t, "test_link", link.ID())

	// 测试 Write 方法 - 使用协程避免阻塞
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 9)
		_ = client.SetReadDeadline(time.Now().Add(time.Second))
		n, err := client.Read(buf)
		require.NoError(t, err)
		require.Equal(t, "test_data", string(buf[:n]))
	}()

	testData := []byte("test_data")
	n, err := link.Write(testData)
	require.NoError(t, err)
	require.Equal(t, len(testData), n)
	wg.Wait()

	// 测试 Link.Read 方法
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond)
		_, _ = client.Write([]byte("reverse"))
	}()

	readBuf := make([]byte, 7)
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	n, err = link.Read(readBuf)
	require.NoError(t, err)
	require.Equal(t, "reverse", string(readBuf[:n]))
	wg.Wait()
}

func TestConcurrentWriteOperations(t *testing.T) {
	t.Parallel()
	const (
		linkCnt    = 10
		msgPerLink = 5
	)

	group := prof.NewPartLinkWriteGroup(t.Context(), 100)
	defer group.Close()

	var wg sync.WaitGroup
	connections := make([]struct {
		client net.Conn
		server net.Conn
	}, 0, linkCnt)

	// 清理连接
	defer func() {
		for _, conn := range connections {
			conn.client.Close()
			conn.server.Close()
		}
	}()

	// 创建多个 Link
	for i := range linkCnt {
		client, server := net.Pipe()
		connections = append(connections, struct {
			client net.Conn
			server net.Conn
		}{client, server})

		id := fmt.Sprintf("concurrent_link_%d", i)
		link := prof.NewLink(id, server)
		group.Add(link)

		// 为每个 link 启动读协程，期望读取多条消息
		wg.Add(1)
		go func(idx int, c net.Conn) {
			defer wg.Done()
			for j := range msgPerLink {
				expected := fmt.Appendf(nil, "concurrent_msg_%d_%d", idx, j)
				buf := make([]byte, len(expected))
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				n, err := io.ReadFull(c, buf)
				require.NoError(t, err, "链接 %d 消息 %d 应该读取成功", idx, j)
				require.Equal(t, expected, buf[:n], "链接 %d 消息 %d 内容不匹配", idx, j)
			}
		}(i, client)
	}

	// 并发写入多条消息到每个 link
	for i := range linkCnt {
		go func(linkIdx int) {
			for j := range msgPerLink {
				id := fmt.Sprintf("concurrent_link_%d", linkIdx)
				msg := fmt.Appendf(nil, "concurrent_msg_%d_%d", linkIdx, j)
				err := group.Write(id, msg)
				require.NoError(t, err, "并发写入链接 %d 消息 %d 应该成功", linkIdx, j)
				time.Sleep(time.Millisecond) // 小延迟模拟真实场景
			}
		}(i)
	}

	// 等待所有读操作完成
	wg.Wait()
}
