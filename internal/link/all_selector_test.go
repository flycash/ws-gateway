//go:build unit

package link_test

import (
	"testing"

	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/internal/link"
	"gitee.com/flycash/ws-gateway/internal/mocks"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

func TestAllLinksSelector(t *testing.T) {
	suite.Run(t, &AllLinksSelectorSuite{})
}

type AllLinksSelectorSuite struct {
	suite.Suite
	selector *link.AllLinksSelector
	ctrl     *gomock.Controller
}

func (s *AllLinksSelectorSuite) SetupTest() {
	s.selector = link.NewAllLinksSelector()
	s.ctrl = gomock.NewController(s.T())
}

func (s *AllLinksSelectorSuite) TearDownTest() {
	s.ctrl.Finish()
}

func (s *AllLinksSelectorSuite) TestNewAllLinksSelector() {
	selector := link.NewAllLinksSelector()
	s.NotNil(selector)
	s.IsType(&link.AllLinksSelector{}, selector)
}

func (s *AllLinksSelectorSuite) TestSelect_EmptyLinks() {
	// 测试空链接列表
	var links []gateway.Link
	result := s.selector.Select(links)

	s.Empty(result)
	s.Equal(0, len(result))
}

func (s *AllLinksSelectorSuite) TestSelect_SingleLink() {
	// 创建模拟 Link
	mockLink := mocks.NewMockLink(s.ctrl)
	links := []gateway.Link{mockLink}

	result := s.selector.Select(links)

	s.Equal(1, len(result))
	s.Equal(mockLink, result[0])
}

func (s *AllLinksSelectorSuite) TestSelect_MultipleLinks() {
	// 创建多个模拟 Link
	mockLink1 := mocks.NewMockLink(s.ctrl)
	mockLink2 := mocks.NewMockLink(s.ctrl)
	mockLink3 := mocks.NewMockLink(s.ctrl)

	links := []gateway.Link{mockLink1, mockLink2, mockLink3}

	result := s.selector.Select(links)

	s.Equal(3, len(result))
	s.Equal(mockLink1, result[0])
	s.Equal(mockLink2, result[1])
	s.Equal(mockLink3, result[2])
}

func (s *AllLinksSelectorSuite) TestSelect_ReturnsOriginalSlice() {
	// 测试返回的是原始切片的引用
	mockLink := mocks.NewMockLink(s.ctrl)
	originalLinks := []gateway.Link{mockLink}

	result := s.selector.Select(originalLinks)

	// 应该返回相同的切片引用
	s.True(&originalLinks[0] == &result[0])
}

func (s *AllLinksSelectorSuite) TestSelect_NilLinks() {
	// 测试 nil 输入
	var links []gateway.Link = nil
	result := s.selector.Select(links)

	s.Nil(result)
}

// 基准测试
func BenchmarkAllLinksSelector_Select(b *testing.B) {
	selector := link.NewAllLinksSelector()
	ctrl := gomock.NewController(b)
	defer ctrl.Finish()

	// 创建100个模拟链接
	links := make([]gateway.Link, 100)
	for i := 0; i < 100; i++ {
		links[i] = mocks.NewMockLink(ctrl)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = selector.Select(links)
	}
}
