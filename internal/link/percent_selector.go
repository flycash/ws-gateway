package link

import gateway "gitee.com/flycash/ws-gateway"

type PercentLinksSelector struct {
	percent float64
}

func NewPercentLinksSelector(percent float64) gateway.LinkSelector {
	if percent < 0 {
		percent = 0
	}
	if percent > 1.0 {
		percent = 1.0
	}
	return &PercentLinksSelector{percent: percent}
}

func (s *PercentLinksSelector) Select(links []gateway.Link) []gateway.Link {
	return NewRandomLinksSelector(int64(float64(len(links)) * s.percent)).Select(links)
}
