package link

import (
	"math/rand/v2"
	"slices"

	gateway "gitee.com/flycash/ws-gateway"
)

type RandomLinksSelector struct {
	count int64
}

func NewRandomLinksSelector(count int64) gateway.LinkSelector {
	return &RandomLinksSelector{count: count}
}

func (s *RandomLinksSelector) Select(links []gateway.Link) []gateway.Link {
	cloned := slices.Clone(links)
	if s.count >= int64(len(cloned)) {
		return cloned
	}
	// Fisher-Yates shuffle 随机打乱
	rand.Shuffle(len(cloned), func(i, j int) {
		cloned[i], cloned[j] = cloned[j], cloned[i]
	})
	return cloned[:s.count]
}
