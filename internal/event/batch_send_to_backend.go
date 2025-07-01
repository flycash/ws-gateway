package event

import (
	"context"
	"encoding/json"
	"time"

	msgv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/ecodeclub/ekit/slice"
	"github.com/ecodeclub/mq-api"
	"github.com/gotomicro/ego/core/elog"
)

const (
	defaultBatchSize = 10
	number0          = 0
	defaultSleepTime = 100 * time.Millisecond
	defaultTimeout   = 3 * time.Second
)

type batchSendToBackendEvent struct {
	Msgs []*msgv1.Message `json:"msgs"`
}

type BatchSendToBackendEventProducer struct {
	producer  mq.Producer
	req       chan batchReq
	batchSize int
	topic     string
	logger    *elog.Component
}

func NewBatchSendToBackendEventProducer(p mq.Producer, topic string) *BatchSendToBackendEventProducer {
	return &BatchSendToBackendEventProducer{
		producer:  p,
		topic:     topic,
		batchSize: defaultBatchSize,
		req:       make(chan batchReq, defaultBatchSize),
	}
}

type batchReq struct {
	Msg     *msgv1.Message
	ErrChan chan error
}

func (p *BatchSendToBackendEventProducer) Produce(ctx context.Context, msg *msgv1.Message) error {
	errch := make(chan error, 1)
	p.req <- batchReq{
		Msg:     msg,
		ErrChan: errch,
	}
	select {
	case err := <-errch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *BatchSendToBackendEventProducer) Start(ctx context.Context) {
	for {
		err := p.oneLoop(ctx)
		if err != nil {
			p.logger.Error("批量发送失败", elog.FieldErr(err))
		}
	}
}

func (p *BatchSendToBackendEventProducer) oneLoop(ctx context.Context) error {
	reqs := make([]batchReq, 0, p.batchSize)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case req := <-p.req:
			reqs = append(reqs, req)
			if len(reqs) < p.batchSize {
				continue
			}
		default:
			// 处理逻辑
			if len(reqs) == number0 {
				time.Sleep(defaultSleepTime)
			}
			ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
			p.process(ctx, reqs)
			cancel()
		}
	}
}

func (p *BatchSendToBackendEventProducer) process(ctx context.Context, reqs []batchReq) {
	msgs := slice.Map(reqs, func(_ int, src batchReq) *msgv1.Message {
		return src.Msg
	})
	var (
		err      error
		evtBytes []byte
	)
	evt := batchSendToBackendEvent{
		Msgs: msgs,
	}
	defer func() {
		for idx := range reqs {

			req := reqs[idx]
			req.ErrChan <- err
		}
	}()
	evtBytes, err = json.Marshal(evt)
	if err != nil {
		return
	}
	_, err = p.producer.Produce(ctx, &mq.Message{
		Value: evtBytes,
		Topic: p.topic,
	})
}
