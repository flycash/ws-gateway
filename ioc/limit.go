package ioc

import (
	"log"

	"github.com/gotomicro/ego/core/econf"
	"golang.org/x/time/rate"
)

func InitRateLimiter() *rate.Limiter {
	type Config struct {
		Rate  int `yaml:"rate"`
		Burst int `yaml:"burst"`
	}
	var cfg Config
	err := econf.UnmarshalKey("server.websocket.limit", &cfg)
	if err != nil {
		panic(err)
	}
	log.Printf("server.websocket.limit: rate = %d, burst = %d \n", cfg.Rate, cfg.Burst)
	return rate.NewLimiter(rate.Limit(cfg.Rate), cfg.Burst)
}
