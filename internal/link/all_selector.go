package link

import gateway "gitee.com/flycash/ws-gateway"

// AllLinksSelector 选择所有连接
type AllLinksSelector struct{}

// NewAllLinksSelector 创建一个选择所有连接的选择器
func NewAllLinksSelector() *AllLinksSelector {
	return &AllLinksSelector{}
}

// Select 选择所有连接
func (s *AllLinksSelector) Select(links []gateway.Link) []gateway.Link {
	return links
}
