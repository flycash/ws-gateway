package ioc

import (
	"log"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/internal/event"
	"gitee.com/flycash/ws-gateway/internal/linkevent"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"gitee.com/flycash/ws-gateway/pkg/encrypt"
	"github.com/ecodeclub/ecache"
	"github.com/ego-component/eetcd"
	"github.com/gotomicro/ego/core/econf"
)

func initLinkEventHandler(
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
		InitInterval time.Duration `yaml:"initInterval"`
		MaxInterval  time.Duration `yaml:"maxInterval"`
		MaxRetries   int32         `yaml:"maxRetries"`
	}
	type Config struct {
		RequestTimeout time.Duration       `yaml:"requestTimeout"`
		RetryStrategy  RetryStrategyConfig `yaml:"retryStrategy"`
		PushMessage    PushMessageConfig   `yaml:"pushMessage"`
	}
	var cfg Config
	err = econf.UnmarshalKey("link.eventHandler", &cfg)
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
		cfg.RetryStrategy.InitInterval,
		cfg.RetryStrategy.MaxInterval,
		cfg.RetryStrategy.MaxRetries,
		cfg.PushMessage.RetryInterval, cfg.PushMessage.MaxRetries)
}

func initUserActionHandler(producer event.UserActionEventProducer) *linkevent.UserActionHandler {
	requestTimeout := econf.GetDuration("link.eventHandler.requestTimeout")
	return linkevent.NewUserActionHandler(
		producer,
		requestTimeout)
}

func initOnlineUserHandler() *linkevent.OnlineUserHandler {
	return linkevent.NewOnlineUserHandler()
}

func InitLinkEventHandlerWrapper(
	cache ecache.Cache,
	codecHelper codec.Codec,
	etcdClient *eetcd.Component,
	producer event.UserActionEventProducer,
) *gateway.LinkEventHandlerWrapper {
	h := initLinkEventHandler(cache, codecHelper, etcdClient)
	uah := initUserActionHandler(producer)
	olh := initOnlineUserHandler()
	return gateway.NewLinkEventHandlerWrapper(h, uah, olh)
}
