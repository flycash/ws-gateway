package ioc

import (
	"log"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/internal/linkevent"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"gitee.com/flycash/ws-gateway/pkg/encrypt"
	"github.com/ecodeclub/ecache"
	"github.com/ego-component/eetcd"
	"github.com/gotomicro/ego/core/econf"
)

func InitLinkEventHandler(
	cache ecache.Cache,
	codecHelper codec.Codec,
	etcdClient *eetcd.Component,
) *linkevent.Handler {
	var encryptConfig encrypt.Config
	err := econf.UnmarshalKey("server.websocket.encrypt", &encryptConfig)
	if err != nil {
		panic(err)
	}

	encryptor, err := encrypt.NewEncryptor(encryptConfig)
	if err != nil {
		panic(err)
	}

	log.Printf("codec = %#v, encryptor = %#v\n", codecHelper, encryptor)

	type PushMessageConfig struct {
		RetryInterval time.Duration `yaml:"retryInterval"`
		MaxRetries    int           `yaml:"maxRetries"`
	}
	type RetryStrategyConfig struct {
		InitRetryInterval time.Duration `yaml:"initRetryInterval"`
		MaxRetryInterval  time.Duration `yaml:"maxRetryInterval"`
		MaxRetries        int32         `yaml:"maxRetries"`
	}
	type Config struct {
		RequestTimeout time.Duration       `yaml:"requestTimeout"`
		RetryStrategy  RetryStrategyConfig `yaml:"retryStrategy"`
		PushMessage    PushMessageConfig   `yaml:"pushMessage"`
	}
	var cfg Config
	err = econf.UnmarshalKey("linkEvent", &cfg)
	if err != nil {
		panic(err)
	}

	cacheRequestTimeout := econf.GetDuration("cache.requestTimeout")
	cacheValueExpiration := econf.GetDuration("cache.valueExpiration")

	return linkevent.NewHandler(
		cache,
		cacheRequestTimeout,
		cacheValueExpiration,
		codecHelper,
		encryptor,
		InitBackendClientLoader(etcdClient),
		cfg.RequestTimeout,
		cfg.RetryStrategy.InitRetryInterval,
		cfg.RetryStrategy.MaxRetryInterval,
		cfg.RetryStrategy.MaxRetries,
		cfg.PushMessage.RetryInterval, cfg.PushMessage.MaxRetries)
}

func InitUserActionHandler() *linkevent.UserActionHandler {
	type Config struct {
		Topic string `yaml:"topic"`
	}
	var cfg Config
	err := econf.UnmarshalKey("userActionEvent", &cfg)
	if err != nil {
		panic(err)
	}
	producer, err := q.Producer(cfg.Topic)
	if err != nil {
		panic(err)
	}
	requestTimeout := econf.GetDuration("linkEvent.requestTimeout")
	return linkevent.NewUserActionHandler(
		linkevent.NewUserActionProducer(producer, cfg.Topic),
		requestTimeout)
}

func InitOnlineUserHandler() *linkevent.OnlineUserHandler {
	return linkevent.NewOnlineUserHandler()
}

func InitLintEventHandlerWrapper(
	h *linkevent.Handler,
	uah *linkevent.UserActionHandler,
	olh *linkevent.OnlineUserHandler,
) *gateway.LinkEventHandlerWrapper {
	return gateway.NewLinkEventHandlerWrapper(h, uah, olh)
}
