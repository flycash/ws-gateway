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
	"github.com/gotomicro/ego/core/elog"
	"golang.org/x/time/rate"
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

func initPrometheusHandler(codecHelper codec.Codec) *linkevent.PrometheusHandler {
	return linkevent.NewPrometheusHandler(codecHelper)
}

func initLimitHandler(c codec.Codec, limiter *rate.Limiter) *linkevent.LimitHandler {
	return linkevent.NewLimitHandler(c, limiter)
}

//nolint:unused // 忽略
func initBatchHandler(
	cache ecache.Cache,
	codecHelper codec.Codec,
	encryptor encrypt.Encryptor,
	etcdClient *eetcd.Component,
) *linkevent.BatchHandler {
	logger := elog.EgoLogger.With(elog.FieldComponent("BatchHandler"))

	type BatchConfig struct {
		// Size 批次大小阈值，默认10个消息
		Size int `json:"size" yaml:"size"`
		// Timeout 批次超时时间，默认500ms
		Timeout time.Duration `json:"timeout" yaml:"timeout"`
	}

	// 读取批处理配置
	var batchConfig BatchConfig
	err := econf.UnmarshalKey("server.websocket.batch", &batchConfig)
	if err != nil {
		logger.Panic("解析批处理配置失败", elog.FieldErr(err))
	}

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

	// 读取其他配置
	cacheRequestTimeout := econf.GetDuration("server.websocket.cache.requestTimeout")
	cacheValueExpiration := econf.GetDuration("server.websocket.cache.valueExpiration")

	// 创建批量后端客户端加载器
	batchBackendClientLoader := InitBatchBackendClientLoader(etcdClient)

	// 创建批量处理器
	batchProcessor := linkevent.NewBatchProcessor(
		batchBackendClientLoader,
		cfg.RequestTimeout,
		cfg.RetryStrategy.InitInterval,
		cfg.RetryStrategy.MaxInterval,
		cfg.RetryStrategy.MaxRetries,
		logger,
	)

	// 创建批处理协调器
	coordinator := linkevent.NewCoordinator(
		batchConfig.Size,
		batchConfig.Timeout,
		batchProcessor,
		logger,
	)

	// 创建批量处理器
	return linkevent.NewBatchHandler(
		cache,
		cacheRequestTimeout,
		cacheValueExpiration,
		codecHelper,
		encryptor,
		coordinator,
		cfg.PushMessage.RetryInterval,
		cfg.PushMessage.MaxRetries,
	)
}

func InitLinkEventHandlerWrapper(
	cache ecache.Cache,
	codecHelper codec.Codec,
	etcdClient *eetcd.Component,
	producer event.UserActionEventProducer,
	limiter *rate.Limiter,
) *gateway.LinkEventHandlerWrapper {
	h := initLinkEventHandler(cache, codecHelper, etcdClient)
	uah := initUserActionHandler(producer)
	olh := initOnlineUserHandler()
	ph := initPrometheusHandler(codecHelper)
	l := initLimitHandler(codecHelper, limiter)
	return gateway.NewLinkEventHandlerWrapper(l, h, uah, olh, ph)
}
