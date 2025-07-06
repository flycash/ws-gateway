package linkevent

import (
	"context"
	"errors"
	"fmt"
	"time"

	gateway "gitee.com/flycash/ws-gateway"
	apiv1 "gitee.com/flycash/ws-gateway/api/proto/gen/gatewayapi/v1"
	"gitee.com/flycash/ws-gateway/pkg/codec"
	"gitee.com/flycash/ws-gateway/pkg/encrypt"
	"gitee.com/flycash/ws-gateway/pkg/pushretry"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"github.com/ecodeclub/ecache"
	"github.com/gotomicro/ego/core/elog"
)

type BatchHandler struct {
	cache                ecache.Cache
	cacheRequestTimeout  time.Duration
	cacheValueExpiration time.Duration

	codecHelper                     codec.Codec
	encryptor                       encrypt.Encryptor
	onFrontendSendMessageHandleFunc map[apiv1.Message_CommandType]func(lk gateway.Link, msg *apiv1.Message) error

	coordinator *Coordinator

	pushRetryManager *pushretry.Manager

	logger *elog.Component
}

// NewBatchHandler 创建一个Link生命周期事件管理器
func NewBatchHandler(
	cache ecache.Cache,
	cacheRequestTimeout time.Duration,
	cacheValueExpiration time.Duration,
	codecHelper codec.Codec,
	encryptor encrypt.Encryptor,
	coordinator *Coordinator,
	pushRetryInterval time.Duration,
	pushMaxRetries int,
) *BatchHandler {
	h := &BatchHandler{
		cache:                           cache,
		cacheRequestTimeout:             cacheRequestTimeout,
		cacheValueExpiration:            cacheValueExpiration,
		codecHelper:                     codecHelper,
		encryptor:                       encryptor,
		onFrontendSendMessageHandleFunc: make(map[apiv1.Message_CommandType]func(lk gateway.Link, msg *apiv1.Message) error),
		coordinator:                     coordinator,
		logger:                          elog.EgoLogger.With(elog.FieldComponent("LinkEvent.BatchHandler")),
	}

	// 初始化重传管理器
	h.pushRetryManager = pushretry.NewManager(
		pushRetryInterval,
		pushMaxRetries,
		h.push, // 将Handler的push方法作为重传函数
	)

	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_HEARTBEAT] = h.handleOnHeartbeatCmd
	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_UPSTREAM_MESSAGE] = h.handleOnUpstreamMessageCmd
	h.onFrontendSendMessageHandleFunc[apiv1.Message_COMMAND_TYPE_DOWNSTREAM_ACK] = h.handleDownstreamAckCmd

	return h
}

func (l *BatchHandler) OnConnect(lk gateway.Link) error {
	// 验证Auth包、协商序列化算法、加密算法、压缩算法
	l.logger.Info("Hello link = " + lk.ID())
	return nil
}

// OnFrontendSendMessage 统一处理前端发来的各种请求
func (l *BatchHandler) OnFrontendSendMessage(lk gateway.Link, payload []byte) error {
	msg, err := l.getMessage(payload)
	if err != nil {
		l.logger.Error("获取消息失败",
			elog.String("step", "OnFrontendSendMessage"),
			elog.String("linkID", lk.ID()),
			elog.Any("userInfo", l.userInfo(lk)),
			elog.FieldErr(err),
		)
		return err
	}

	// 消息幂等
	bizID := l.userInfo(lk).BizID
	ok, err := l.cacheMessage(bizID, msg)
	if err != nil {
		err = fmt.Errorf("%w; %w", ErrCacheFrontendMessageFailed, err)
		l.logger.Error("缓存消息失败",
			elog.String("step", "getMessage"),
			elog.String("消息体", msg.String()),
			elog.FieldErr(err),
		)
		return err
	} else if !ok {
		err = fmt.Errorf("%w", ErrDuplicatedFrontendMessage)
		l.logger.Warn("重复消息，已丢弃",
			elog.String("step", "getMessage"),
			elog.String("消息体", msg.String()),
			elog.FieldErr(err),
		)
		return err
	}

	l.logger.Info("OnFrontendSendMessage",
		elog.String("step", "前端发送的消息(上行消息+对下行消息的响应)"),
		elog.String("消息体", msg.String()))

	// 前端发送的消息(心跳、上行消息及对下行消息的确认) 统一在这里处理
	handleFunc, ok := l.onFrontendSendMessageHandleFunc[msg.Cmd]
	if !ok {
		l.logger.Error("前端发送未知消息类型",
			elog.String("step", "OnFrontendSendMessage"),
			elog.String("linkID", lk.ID()),
			elog.Any("userInfo", l.userInfo(lk)),
		)
		return fmt.Errorf("%w", ErrUnKnownFrontendMessageCommandType)
	}
	err = handleFunc(lk, msg)
	if err == nil {
		return nil
	}
	// 只有在特定错误类型下才删除缓存
	if l.shouldDeleteCacheOnError(err) {
		if err1 := l.deleteCacheMessage(bizID, msg); err1 != nil {
			l.logger.Warn("删除消息缓存失败",
				elog.String("step", "OnFrontendSendMessage"),
				elog.String("linkID", lk.ID()),
				elog.String("msg", msg.String()),
				elog.FieldErr(err1),
			)
		}
	}
	return err
}

func (l *BatchHandler) userInfo(lk gateway.Link) session.UserInfo {
	return lk.Session().UserInfo()
}

func (l *BatchHandler) getMessage(payload []byte) (*apiv1.Message, error) {
	msg := &apiv1.Message{}
	// 反序列化
	err := l.codecHelper.Unmarshal(payload, msg)
	if err != nil || (msg.GetKey() == "" && msg.GetCmd() != apiv1.Message_COMMAND_TYPE_HEARTBEAT) {
		l.logger.Error("反序列化消息失败",
			elog.String("step", "getMessage"),
			elog.Any("codecHelper", l.codecHelper.Name()),
			elog.String("消息体", msg.String()),
			elog.FieldErr(err),
		)
		return nil, fmt.Errorf("%w", ErrUnKnownFrontendMessageFormat)
	}

	// 解密消息体
	decryptedBody, err := l.encryptor.Decrypt(msg.GetBody())
	if err != nil {
		l.logger.Error("解密消息体失败",
			elog.String("step", "getMessage"),
			elog.String("encryptor", l.encryptor.Name()),
			elog.String("消息体", msg.String()),
			elog.FieldErr(err),
		)
		return nil, fmt.Errorf("%w: %w", ErrDecryptMessageBodyFailed, err)
	}
	msg.Body = decryptedBody

	return msg, nil
}

func (l *BatchHandler) cacheMessage(bizID int64, msg *apiv1.Message) (bool, error) {
	if msg.GetCmd() == apiv1.Message_COMMAND_TYPE_HEARTBEAT {
		// 心跳消息不需要缓存
		return true, nil
	}
	ctx, cancelFunc := context.WithTimeout(context.Background(), l.cacheRequestTimeout)
	defer cancelFunc()
	return l.cache.SetNX(ctx, l.cacheKey(bizID, msg), msg.GetKey(), l.cacheValueExpiration)
}

func (l *BatchHandler) cacheKey(bizID int64, msg *apiv1.Message) string {
	return fmt.Sprintf("%d-%s", bizID, msg.GetKey())
}

// 判断是否应该在错误时删除缓存
func (l *BatchHandler) shouldDeleteCacheOnError(err error) bool {
	// 消息已缓存但业务逻辑无法正常执行时删除缓存，允许前端重试
	return errors.Is(err, ErrUnknownBizID) ||
		errors.Is(err, ErrEncryptMessageBodyFailed) ||
		errors.Is(err, ErrMarshalMessageFailed) ||
		errors.Is(err, ErrMaxRetriesExceeded)
}

func (l *BatchHandler) deleteCacheMessage(bizID int64, msg *apiv1.Message) error {
	if msg.GetCmd() == apiv1.Message_COMMAND_TYPE_HEARTBEAT {
		// 心跳消息不需要删除缓存
		return nil
	}
	ctx, cancelFunc := context.WithTimeout(context.Background(), l.cacheRequestTimeout)
	defer cancelFunc()
	_, err := l.cache.Delete(ctx, l.cacheKey(bizID, msg))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDeleteCachedFrontendMessageFailed, err)
	}
	return nil
}

// handleOnHeartbeatCmd 处理前端发来的"心跳"请求
func (l *BatchHandler) handleOnHeartbeatCmd(lk gateway.Link, msg *apiv1.Message) error {
	// 心跳包原样返回
	l.logger.Info("收到心跳包，原样返回",
		elog.String("step", "handleOnHeartbeatCmd"),
		elog.String("linkID", lk.ID()),
		elog.Any("userInfo", l.userInfo(lk)),
		elog.String("消息体", msg.String()))
	return l.push(lk, msg)
}

//nolint:dupl // 忽略
func (l *BatchHandler) push(lk gateway.Link, msg *apiv1.Message) error {
	// 加密消息体后发送给前端
	encryptedBody, err := l.encryptor.Encrypt(msg.GetBody())
	if err != nil {
		l.logger.Error("加密消息体失败",
			elog.String("step", "push"),
			elog.String("encryptor", l.encryptor.Name()),
			elog.String("消息体", msg.String()),
			elog.FieldErr(err),
		)
		return fmt.Errorf("%w: %w", ErrEncryptMessageBodyFailed, err)
	}
	msg.Body = encryptedBody

	payload, err := l.codecHelper.Marshal(msg)
	if err != nil {
		l.logger.Error("序列化网关消息失败",
			elog.String("step", "push"),
			elog.String("codecHelper", l.codecHelper.Name()),
			elog.String("消息体", msg.String()),
			elog.FieldErr(err),
		)
		return fmt.Errorf("%w: %w", ErrMarshalMessageFailed, err)
	}
	// 内部已实现重试
	err = lk.Send(payload)
	if err != nil {
		l.logger.Error("通过link对象下推消息给前端用户失败",
			elog.String("step", "push"),
			elog.String("消息体", msg.String()),
			elog.String("linkID", lk.ID()),
			elog.Any("userInfo", l.userInfo(lk)),
			elog.FieldErr(err))
		return err
	}
	return nil
}

// handleOnUpstreamMessageCmd 处理前端发来的"上行业务消息"请求
func (l *BatchHandler) handleOnUpstreamMessageCmd(lk gateway.Link, msg *apiv1.Message) error {
	// 收到有意义的上行消息，更新活跃时间
	lk.UpdateActiveTime()

	bizID := l.userInfo(lk).BizID

	resp, err := l.coordinator.OnReceive(bizID, lk.ID(), &apiv1.OnReceiveRequest{
		Key:  msg.GetKey(),
		Body: msg.GetBody(),
	})
	if err != nil {
		// 向业务后端转发失败，（包含已经重试）如何处理？ 这里返回err相当于丢掉了，等待前端超时重试
		l.logger.Warn("向业务后端转发消息失败",
			elog.String("step", "handleOnUpstreamMessageCmd"),
			elog.FieldErr(err),
		)
		return err
	}
	return l.sendUpstreamMessageAck(lk, msg.GetKey(), resp)
}

func (l *BatchHandler) sendUpstreamMessageAck(lk gateway.Link, key string, resp *apiv1.OnReceiveResponse) error {
	// 将业务后端返回的"上行消息"的响应直接封装为body
	respBody, err := l.codecHelper.Marshal(resp)
	if err != nil {
		l.logger.Error("序列化业务后端响应失败",
			elog.String("step", "sendUpstreamMessageAck"),
			elog.FieldErr(err),
		)
		return fmt.Errorf("%w: %w", ErrMarshalMessageFailed, err)
	}

	err = l.push(lk, &apiv1.Message{
		Cmd: apiv1.Message_COMMAND_TYPE_UPSTREAM_ACK,
		// 直接用原来的 key
		Key: key,
		// Key:  fmt.Sprintf("%d-%d", resp.GetBizId(), resp.GetMsgId()),
		Body: respBody,
	})
	if err != nil {
		l.logger.Error("向前端下推对上行消息的确认失败",
			elog.String("step", "sendUpstreamMessageAck"),
			elog.FieldErr(err),
		)
		return err
	}
	return nil
}

// handleDownstreamAckCmd 处理前端发来的"对下行消息的确认"请求
func (l *BatchHandler) handleDownstreamAckCmd(lk gateway.Link, msg *apiv1.Message) error {
	// 停止重传任务
	l.pushRetryManager.Stop(l.retryKey(l.userInfo(lk).BizID, msg.GetKey()))

	// 这里可以考虑通知业务后端下行消息的发送结果 如 使用 BackendService.OnPushed 方法
	// 也可以考虑使用消息队列通知业务后端，规避GRPC客户端的各种重试、超时问题，并保证高吞吐量
	// 开启body加密后，需要先解密再调用业务后端
	l.logger.Info("收到下行消息确认",
		elog.String("step", "handleDownstreamAckCmd"),
		elog.String("linkID", lk.ID()),
		elog.Any("userInfo", l.userInfo(lk)),
		elog.String("msg", msg.String()),
	)
	return nil
}

func (l *BatchHandler) retryKey(bizID int64, key string) string {
	return fmt.Sprintf("%d-%s", bizID, key)
}

// OnBackendPushMessage 统一处理各个业务后端发来的下推请求
func (l *BatchHandler) OnBackendPushMessage(lk gateway.Link, msg *apiv1.PushMessage) error {
	if msg.GetBizId() == 0 || msg.GetKey() == "" {
		return fmt.Errorf("%w", ErrUnKnownBackendMessageFormat)
	}

	message := &apiv1.Message{
		Cmd:  apiv1.Message_COMMAND_TYPE_DOWNSTREAM_MESSAGE,
		Key:  msg.GetKey(),
		Body: msg.GetBody(),
	}

	// 无论推送成功与否都启动重传任务，等待前端ACK确认后才停止
	defer l.pushRetryManager.Start(l.retryKey(msg.GetBizId(), msg.GetKey()), lk, message)

	err := l.push(lk, message)
	if err != nil {
		// 启动重传任务
		l.logger.Error("向前端推送下行消息失败",
			elog.String("step", "OnBackendPushMessage"),
			elog.FieldErr(err),
		)
		return fmt.Errorf("%w", err)
	}

	// 成功推送下行消息，更新活跃时间
	lk.UpdateActiveTime()
	return nil
}

func (l *BatchHandler) OnDisconnect(lk gateway.Link) error {
	// 退出清理操作
	// 清理该连接的重传任务
	l.pushRetryManager.StopByLinkID(lk.ID())
	l.logger.Info("Goodbye link = " + lk.ID())
	return lk.Close()
}
