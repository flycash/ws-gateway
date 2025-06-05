package ioc

import (
	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/ecache/memory/lru"
	"github.com/gotomicro/ego/core/econf"
)

func InitLocalCache() ecache.Cache {
	type Config struct {
		Capacity int `yaml:"capacity"`
	}
	var cfg Config
	err := econf.UnmarshalKey("cache.local", &cfg)
	if err != nil {
		panic(err)
	}
	return &ecache.NamespaceCache{
		C:         lru.NewCache(cfg.Capacity),
		Namespace: "ws-gateway",
	}
}
