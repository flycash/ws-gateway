package ioc

import (
	"github.com/ecodeclub/ecache"
	redisecache "github.com/ecodeclub/ecache/redis"
	"github.com/gotomicro/ego/core/econf"
	"github.com/redis/go-redis/v9"
)

func InitRedisClient() *redis.Client {
	type Config struct {
		Addr string
	}
	var cfg Config
	err := econf.UnmarshalKey("redis", &cfg)
	if err != nil {
		panic(err)
	}
	cmd := redis.NewClient(&redis.Options{
		Addr: cfg.Addr,
	})
	// cmd = tracing.WithTracing(cmd)
	// cmd = metrics.WithMetrics(cmd)
	return cmd
}

func InitRedisCmd() redis.Cmdable {
	type Config struct {
		Addr string
	}
	var cfg Config
	err := econf.UnmarshalKey("redis", &cfg)
	if err != nil {
		panic(err)
	}
	cmd := redis.NewClient(&redis.Options{
		Addr: cfg.Addr,
	})
	// cmd = tracing.WithTracing(cmd)
	// cmd = metrics.WithMetrics(cmd)
	return cmd
}

func InitRedisCache(r redis.Cmdable) ecache.Cache {
	return redisecache.NewCache(r)
}
