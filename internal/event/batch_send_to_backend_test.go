//go:build e2e

package event

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	msgv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"github.com/ecodeclub/mq-api"
	"github.com/ecodeclub/mq-api/memory"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type BatchSendToBackendTestSuite struct {
	suite.Suite
	producer *BatchSendToBackendEventProducer
	topic    string
	testmq   mq.MQ
}

func initMQ() (mq.MQ, error) {
	type Topic struct {
		Name       string `yaml:"name"`
		Partitions int    `yaml:"partitions"`
	}
	topics := []Topic{
		{
			Name:       "im_async_topic",
			Partitions: 1,
		},
	}
	// 替换用内存实现，方便测试
	qq := memory.NewMQ()
	for _, t := range topics {
		err := qq.CreateTopic(context.Background(), t.Name, t.Partitions)
		if err != nil {
			return nil, err
		}
	}
	return qq, nil
}

func (s *BatchSendToBackendTestSuite) SetupTest() {
	testmq := memory.NewMQ()
	topic := "im_async_topic"
	s.topic = topic
	producer, err := testmq.Producer(topic)
	require.NoError(s.T(), err)
	s.producer = NewBatchSendToBackendEventProducer(producer, s.topic)
	s.testmq = testmq
}

func (s *BatchSendToBackendTestSuite) TestBatchSendToBackend() {
	// 测试逻辑
	// 通过 producer发送 20条消息，最后通过消费者都能接收到数据

	// 创建消费者
	consumer, err := s.testmq.Consumer(s.topic, "test-consumer")
	require.NoError(s.T(), err)

	msgChan, err := consumer.ConsumeChan(s.T().Context())
	require.NoError(s.T(), err)

	// 启动生产者
	producerCtx, producerCancel := context.WithCancel(s.T().Context())
	defer producerCancel()
	go s.producer.Start(producerCtx)

	// 创建20条测试消息
	testMessages := make([]*msgv1.Message, 20)
	for i := 0; i < 20; i++ {
		testMessages[i] = &msgv1.Message{
			Cmd:  msgv1.Message_COMMAND_TYPE_UPSTREAM_MESSAGE,
			Key:  fmt.Sprintf("test-key-%d", i),
			Body: []byte(fmt.Sprintf("test-body-%d", i)),
		}
	}

	// 发送消息
	var wg sync.WaitGroup
	for _, msg := range testMessages {
		wg.Add(1)
		go func(m *msgv1.Message) {
			defer wg.Done()
			err := s.producer.Produce(s.T().Context(), m)
			require.NoError(s.T(), err)
		}(msg)
	}
	wg.Wait()

	// 等待一段时间让批量处理完成
	time.Sleep(200 * time.Millisecond)

	// 收集接收到的消息
	var receivedEvents []batchSendToBackendEvent
	ctx, cancel := context.WithTimeout(s.T().Context(), 5*time.Second)
	defer cancel()

	for {
		select {
		case msg := <-msgChan:
			var event batchSendToBackendEvent
			err := json.Unmarshal(msg.Value, &event)
			require.NoError(s.T(), err)
			receivedEvents = append(receivedEvents, event)

			// 检查是否收到了所有消息
			totalReceived := 0
			for _, evt := range receivedEvents {
				totalReceived += len(evt.Msgs)
			}
			if totalReceived >= 20 {
				// 验证接收到的消息
				s.validateReceivedMessages(testMessages, receivedEvents)
				return
			}
		case <-ctx.Done():
			s.T().Fatalf("超时：期望接收20条消息，实际接收到的消息数量不足")
		}
	}
}

func (s *BatchSendToBackendTestSuite) validateReceivedMessages(expected []*msgv1.Message, receivedEvents []batchSendToBackendEvent) {
	// 验证每条消息的内容
	receivedMap := make(map[string]*msgv1.Message)
	for _, evt := range receivedEvents {
		for _, msg := range evt.Msgs {
			receivedMap[msg.GetKey()] = msg
		}
	}

	// 验证所有期望的消息都被接收到了
	for _, expectedMsg := range expected {
		receivedMsg, exists := receivedMap[expectedMsg.GetKey()]
		require.True(s.T(), exists, "消息 %s 未被接收到", expectedMsg.GetKey())
		require.Equal(s.T(), expectedMsg.GetCmd(), receivedMsg.GetCmd(), "消息命令类型不匹配")
		require.Equal(s.T(), expectedMsg.GetBody(), receivedMsg.GetBody(), "消息体不匹配")
	}
}

func TestBatchSend(t *testing.T) {
	suite.Run(t, new(BatchSendToBackendTestSuite))
}
