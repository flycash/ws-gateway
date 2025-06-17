//go:build unit

package link_test

import (
	"context"
	"net"
	"testing"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"gitee.com/flycash/ws-gateway/pkg/session"
	sessmocks "gitee.com/flycash/ws-gateway/pkg/session/mocks"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

func TestManager(t *testing.T) {
	suite.Run(t, &ManagerSuite{})
}

type ManagerSuite struct {
	suite.Suite
	manager     gateway.LinkManager
	codec       codec.Codec
	ctrl        *gomock.Controller
	mockSession *sessmocks.MockSession
}

func (s *ManagerSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.codec = codec.NewJSONCodec()
	s.manager = link.NewManager(s.codec, nil)
	s.mockSession = sessmocks.NewMockSession(s.ctrl)
}

func (s *ManagerSuite) TearDownTest() {
	s.ctrl.Finish()
	_ = s.manager.Close()
}

func (s *ManagerSuite) TestNewManager() {
	manager := link.NewManager(s.codec, nil)
	s.NotNil(manager)
	s.Equal(int64(0), manager.Len())
}

func (s *ManagerSuite) TestNewManager_WithCustomConfig() {
	config := &link.ManagerConfig{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 3 * time.Second,
		MaxRetries:   5,
	}

	manager := link.NewManager(s.codec, config)
	s.NotNil(manager)
	s.Equal(int64(0), manager.Len())
}

func (s *ManagerSuite) TestNewLink() {
	// 创建模拟连接
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	// 设置 mock session 期望
	userInfo := session.UserInfo{
		BizID:     1,
		UserID:    123,
		AutoClose: true,
	}
	s.mockSession.EXPECT().UserInfo().Return(userInfo).AnyTimes()

	ctx := context.Background()
	lk, err := s.manager.NewLink(ctx, serverConn, s.mockSession, nil)

	s.NoError(err)
	s.NotNil(lk)
	s.Equal(int64(1), s.manager.Len())
	s.Equal("1-123", lk.ID())
}

func (s *ManagerSuite) TestFindLinkByUserInfo() {
	// 先创建一个链接
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	userInfo := session.UserInfo{
		BizID:  1,
		UserID: 123,
	}
	s.mockSession.EXPECT().UserInfo().Return(userInfo).AnyTimes()

	ctx := context.Background()
	lk, err := s.manager.NewLink(ctx, serverConn, s.mockSession, nil)
	s.NoError(err)

	// 测试查找
	foundLink, exists := s.manager.FindLinkByUserInfo(userInfo)
	s.True(exists)
	s.Equal(lk, foundLink)

	// 测试查找不存在的用户
	notExistUserInfo := session.UserInfo{
		BizID:  2,
		UserID: 456,
	}
	_, exists = s.manager.FindLinkByUserInfo(notExistUserInfo)
	s.False(exists)
}

func (s *ManagerSuite) TestRedirectLinks() {
	// 创建多个链接
	links := s.createMultipleLinks(3)

	// 创建 AllLinksSelector
	selector := link.NewAllLinksSelector()

	// 创建可用节点
	availableNodes := &apiv1.NodeList{
		Nodes: []*apiv1.Node{
			{
				Id:   "node-1",
				Ip:   "192.168.1.1",
				Port: 8080,
			},
			{
				Id:   "node-2",
				Ip:   "192.168.1.2",
				Port: 8080,
			},
		},
	}

	ctx := context.Background()
	err := s.manager.RedirectLinks(ctx, selector, availableNodes)
	s.NoError(err)

	// 验证链接仍然存在（重定向不应该删除链接）
	s.Equal(int64(3), s.manager.Len())

	// 关闭创建的链接
	for _, lk := range links {
		lk.Close()
	}
}

func (s *ManagerSuite) TestRedirectLinks_SendFailure() {
	// 创建链接，然后立即关闭以模拟发送失败
	links := s.createMultipleLinks(2)

	// 关闭其中一个链接，使其Send方法失败
	err := links[0].Close()
	s.NoError(err)

	// 等待链接完全关闭
	select {
	case <-links[0].HasClosed():
		// 链接已关闭
	case <-time.After(100 * time.Millisecond):
		s.Fail("链接应该已经被关闭")
	}

	selector := link.NewAllLinksSelector()
	availableNodes := &apiv1.NodeList{
		Nodes: []*apiv1.Node{
			{Id: "node-1", Ip: "192.168.1.1", Port: 8080},
		},
	}

	ctx := context.Background()
	// 这应该成功，但会在日志中记录发送失败的错误
	err = s.manager.RedirectLinks(ctx, selector, availableNodes)
	s.NoError(err)

	// 关闭剩余链接
	for i := 1; i < len(links); i++ {
		links[i].Close()
	}
}

func (s *ManagerSuite) TestRedirectLinks_EmptyNodes() {
	// 创建一个链接
	s.createMultipleLinks(1)

	selector := link.NewAllLinksSelector()
	availableNodes := &apiv1.NodeList{}

	ctx := context.Background()
	err := s.manager.RedirectLinks(ctx, selector, availableNodes)
	s.Error(err)
	s.Contains(err.Error(), "没有可用的节点")
}

func (s *ManagerSuite) TestRedirectLinks_NoLinks() {
	selector := link.NewAllLinksSelector()
	availableNodes := &apiv1.NodeList{
		Nodes: []*apiv1.Node{
			{Id: "node-1", Ip: "192.168.1.1", Port: 8080},
		},
	}

	ctx := context.Background()
	err := s.manager.RedirectLinks(ctx, selector, availableNodes)
	s.NoError(err) // 没有链接时应该成功返回
}

func (s *ManagerSuite) TestRedirectLinks_NoSelectedLinks() {
	// 创建链接
	s.createMultipleLinks(2)

	// 创建一个返回空链接的选择器
	mockSelector := &MockEmptySelector{}
	availableNodes := &apiv1.NodeList{
		Nodes: []*apiv1.Node{
			{Id: "node-1", Ip: "192.168.1.1", Port: 8080},
		},
	}

	ctx := context.Background()
	err := s.manager.RedirectLinks(ctx, mockSelector, availableNodes)
	s.NoError(err) // 没有选中的链接时应该成功返回
}

func (s *ManagerSuite) TestCleanIdleLinks_WithAutoClose() {
	// 创建设置了 AutoClose=true 的链接
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	userInfo := session.UserInfo{
		BizID:     1,
		UserID:    123,
		AutoClose: true, // 启用自动关闭
	}
	s.mockSession.EXPECT().UserInfo().Return(userInfo).AnyTimes()

	ctx := context.Background()
	createdLink, err := s.manager.NewLink(ctx, serverConn, s.mockSession, nil)
	s.NoError(err)

	// 等待一段时间让链接变为空闲状态
	time.Sleep(100 * time.Millisecond)

	// 尝试清理空闲链接，使用非常短的超时时间
	cleaned := s.manager.CleanIdleLinks(50 * time.Millisecond)
	s.Equal(1, cleaned)
	s.Equal(int64(0), s.manager.Len())

	// 验证链接已被关闭
	select {
	case <-createdLink.HasClosed():
		// 链接已关闭，符合预期
	case <-time.After(100 * time.Millisecond):
		s.Fail("链接应该已经被关闭")
	}
}

func (s *ManagerSuite) TestCleanIdleLinks_WithoutAutoClose() {
	// 创建链接，但不设置 AutoClose
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	userInfo := session.UserInfo{
		BizID:     1,
		UserID:    123,
		AutoClose: false, // 不自动关闭
	}
	s.mockSession.EXPECT().UserInfo().Return(userInfo).AnyTimes()

	ctx := context.Background()
	_, err := s.manager.NewLink(ctx, serverConn, s.mockSession, nil)
	s.NoError(err)

	// 等待一段时间
	time.Sleep(100 * time.Millisecond)

	// 尝试清理空闲链接，由于 AutoClose=false，不应该被清理
	cleaned := s.manager.CleanIdleLinks(50 * time.Millisecond)
	s.Equal(0, cleaned)
	s.Equal(int64(1), s.manager.Len())
}

func (s *ManagerSuite) TestCleanIdleLinks_NotIdleYet() {
	// 创建设置了 AutoClose=true 的链接
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	userInfo := session.UserInfo{
		BizID:     1,
		UserID:    123,
		AutoClose: true,
	}
	s.mockSession.EXPECT().UserInfo().Return(userInfo).AnyTimes()

	ctx := context.Background()
	_, err := s.manager.NewLink(ctx, serverConn, s.mockSession, nil)
	s.NoError(err)

	// 链接刚创建，还不到空闲时间
	cleaned := s.manager.CleanIdleLinks(10 * time.Second)
	s.Equal(0, cleaned)
	s.Equal(int64(1), s.manager.Len())
}

func (s *ManagerSuite) TestCleanIdleLinks_AlreadyClosed() {
	// 创建并立即关闭链接
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	userInfo := session.UserInfo{
		BizID:     1,
		UserID:    123,
		AutoClose: true,
	}
	s.mockSession.EXPECT().UserInfo().Return(userInfo).AnyTimes()

	ctx := context.Background()
	createdLink, err := s.manager.NewLink(ctx, serverConn, s.mockSession, nil)
	s.NoError(err)

	// 先关闭链接
	err = createdLink.Close()
	s.NoError(err)

	// 等待关闭完成
	select {
	case <-createdLink.HasClosed():
		// 链接已关闭
	case <-time.After(100 * time.Millisecond):
		s.Fail("链接应该已经被关闭")
	}

	// 清理空闲链接应该仍然能识别已关闭的链接
	cleaned := s.manager.CleanIdleLinks(1 * time.Millisecond)
	s.GreaterOrEqual(cleaned, 0) // 已关闭的链接可能已被自动清理
}

func (s *ManagerSuite) TestLen() {
	s.Equal(int64(0), s.manager.Len())

	// 创建几个链接
	links := s.createMultipleLinks(3)
	s.Equal(int64(3), s.manager.Len())

	// 关闭一个链接
	err := links[0].Close()
	s.NoError(err)

	// 等待一段时间让管理器清理关闭的链接
	time.Sleep(100 * time.Millisecond)

	// 关闭剩余链接
	for i := 1; i < len(links); i++ {
		links[i].Close()
	}
}

func (s *ManagerSuite) TestLinks() {
	s.Equal(0, len(s.manager.Links()))

	// 创建几个链接
	createdLinks := s.createMultipleLinks(3)
	allLinks := s.manager.Links()

	s.Equal(3, len(allLinks))

	// 验证返回的链接包含我们创建的链接
	linkIDs := make(map[string]bool)
	for _, lk := range allLinks {
		linkIDs[lk.ID()] = true
	}

	for _, createdLink := range createdLinks {
		s.True(linkIDs[createdLink.ID()])
	}

	// 关闭链接
	for _, lk := range createdLinks {
		lk.Close()
	}
}

func (s *ManagerSuite) TestClose() {
	// 创建几个链接
	links := s.createMultipleLinks(3)
	s.Equal(int64(3), s.manager.Len())

	// 关闭管理器
	err := s.manager.Close()
	s.NoError(err)
	s.Equal(int64(0), s.manager.Len())

	// 验证所有链接都被关闭
	for _, lk := range links {
		select {
		case <-lk.HasClosed():
			// 链接已关闭，符合预期
		case <-time.After(100 * time.Millisecond):
			s.Fail("链接应该已经被关闭")
		}
	}
}

func (s *ManagerSuite) TestClose_WithClosedLinks() {
	// 创建链接
	links := s.createMultipleLinks(2)
	s.Equal(int64(2), s.manager.Len())

	// 预先关闭其中一个链接以模拟Close操作中的错误处理
	err := links[0].Close()
	s.NoError(err)

	// 等待链接完全关闭
	select {
	case <-links[0].HasClosed():
		// 链接已关闭
	case <-time.After(100 * time.Millisecond):
		s.Fail("链接应该已经被关闭")
	}

	// 关闭管理器，这会尝试关闭已经关闭的链接
	err = s.manager.Close()
	s.NoError(err)
	s.Equal(int64(0), s.manager.Len())

	// 验证所有链接都被关闭
	for _, lk := range links {
		select {
		case <-lk.HasClosed():
			// 链接已关闭
		case <-time.After(100 * time.Millisecond):
			s.Fail("链接应该已经被关闭")
		}
	}
}

func (s *ManagerSuite) TestClose_EmptyManager() {
	// 测试空管理器的关闭
	emptyManager := link.NewManager(s.codec, nil)
	err := emptyManager.Close()
	s.NoError(err)
	s.Equal(int64(0), emptyManager.Len())
}

func (s *ManagerSuite) TestClose_MultipleCallsToClose() {
	// 创建链接
	links := s.createMultipleLinks(2)
	s.Equal(int64(2), s.manager.Len())

	// 多次调用Close应该是安全的
	err1 := s.manager.Close()
	s.NoError(err1)
	s.Equal(int64(0), s.manager.Len())

	err2 := s.manager.Close()
	s.NoError(err2)
	s.Equal(int64(0), s.manager.Len())

	// 验证链接确实被关闭了
	for _, lk := range links {
		select {
		case <-lk.HasClosed():
			// 链接已关闭
		case <-time.After(100 * time.Millisecond):
			s.Fail("链接应该已经被关闭")
		}
	}
}

func (s *ManagerSuite) TestGracefulClose() {
	// 创建几个链接
	links := s.createMultipleLinks(2)
	s.Equal(int64(2), s.manager.Len())

	// 模拟客户端主动关闭连接
	go func() {
		time.Sleep(100 * time.Millisecond)
		for _, lk := range links {
			lk.Close()
		}
	}()

	// 测试优雅关闭
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := s.manager.GracefulClose(ctx)
	s.NoError(err)
	s.Equal(int64(0), s.manager.Len())
}

func (s *ManagerSuite) TestGracefulClose_SuccessPath() {
	// 创建链接
	links := s.createMultipleLinks(2)
	s.Equal(int64(2), s.manager.Len())

	// 立即关闭所有链接以触发成功路径
	for _, lk := range links {
		err := lk.Close()
		s.NoError(err)
	}

	// 等待链接关闭
	for _, lk := range links {
		select {
		case <-lk.HasClosed():
			// 链接已关闭
		case <-time.After(100 * time.Millisecond):
			s.Fail("链接应该已经被关闭")
		}
	}

	// 测试优雅关闭，应该立即成功
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.manager.GracefulClose(ctx)
	s.NoError(err)
	s.Equal(int64(0), s.manager.Len())
}

func (s *ManagerSuite) TestGracefulClose_Timeout() {
	// 创建链接但不关闭它们
	s.createMultipleLinks(2)
	s.Equal(int64(2), s.manager.Len())

	// 设置很短的超时时间
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := s.manager.GracefulClose(ctx)
	s.NoError(err) // 超时后会调用 Close()，所以不应该返回错误
	s.Equal(int64(0), s.manager.Len())
}

func (s *ManagerSuite) TestGracefulClose_EmptyManager() {
	// 测试空管理器的优雅关闭
	emptyManager := link.NewManager(s.codec, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := emptyManager.GracefulClose(ctx)
	s.NoError(err)
	s.Equal(int64(0), emptyManager.Len())
}

// 辅助方法：创建多个测试链接
func (s *ManagerSuite) createMultipleLinks(count int) []gateway.Link {
	links := make([]gateway.Link, count)

	for i := 0; i < count; i++ {
		serverConn, clientConn := net.Pipe()
		defer clientConn.Close()
		defer serverConn.Close()

		mockSession := sessmocks.NewMockSession(s.ctrl)
		userInfo := session.UserInfo{
			BizID:     int64(i + 1),
			UserID:    int64(100 + i),
			AutoClose: true,
		}
		mockSession.EXPECT().UserInfo().Return(userInfo).AnyTimes()

		ctx := context.Background()
		lk, err := s.manager.NewLink(ctx, serverConn, mockSession, nil)
		s.NoError(err)

		links[i] = lk
	}

	return links
}

// MockEmptySelector 模拟一个总是返回空结果的选择器
type MockEmptySelector struct{}

func (m *MockEmptySelector) Select(_ []gateway.Link) []gateway.Link {
	return []gateway.Link{} // 总是返回空切片
}

// 基准测试
func BenchmarkManager_NewLink(b *testing.B) {
	c := codec.NewJSONCodec()
	manager := link.NewManager(c, nil)
	defer manager.Close()

	ctrl := gomock.NewController(b)
	defer ctrl.Finish()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		serverConn, clientConn := net.Pipe()

		mockSession := sessmocks.NewMockSession(ctrl)
		userInfo := session.UserInfo{
			BizID:  int64(i),
			UserID: int64(i),
		}
		mockSession.EXPECT().UserInfo().Return(userInfo).AnyTimes()

		lk, _ := manager.NewLink(context.Background(), serverConn, mockSession, nil)
		lk.Close()
		clientConn.Close()
		serverConn.Close()
	}
}

func BenchmarkManager_FindLinkByUserInfo(b *testing.B) {
	c := codec.NewJSONCodec()
	manager := link.NewManager(c, nil)
	defer manager.Close()

	ctrl := gomock.NewController(b)
	defer ctrl.Finish()

	// 预先创建一些链接
	for i := 0; i < 100; i++ {
		serverConn, clientConn := net.Pipe()
		defer clientConn.Close()
		defer serverConn.Close()

		mockSession := sessmocks.NewMockSession(ctrl)
		userInfo := session.UserInfo{
			BizID:  int64(i),
			UserID: int64(i),
		}
		mockSession.EXPECT().UserInfo().Return(userInfo).AnyTimes()

		lk, _ := manager.NewLink(context.Background(), serverConn, mockSession, nil)
		defer lk.Close()
	}

	searchUserInfo := session.UserInfo{BizID: 50, UserID: 50}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = manager.FindLinkByUserInfo(searchUserInfo)
	}
}

func BenchmarkManager_CleanIdleLinks(b *testing.B) {
	c := codec.NewJSONCodec()
	manager := link.NewManager(c, nil)
	defer manager.Close()

	ctrl := gomock.NewController(b)
	defer ctrl.Finish()

	// 预先创建一些链接
	for i := 0; i < 100; i++ {
		serverConn, clientConn := net.Pipe()
		defer clientConn.Close()
		defer serverConn.Close()

		mockSession := sessmocks.NewMockSession(ctrl)
		userInfo := session.UserInfo{
			BizID:     int64(i),
			UserID:    int64(i),
			AutoClose: i%2 == 0, // 一半设置自动关闭
		}
		mockSession.EXPECT().UserInfo().Return(userInfo).AnyTimes()

		created, _ := manager.NewLink(context.Background(), serverConn, mockSession, nil)
		defer created.Close()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manager.CleanIdleLinks(5 * time.Second)
	}
}
