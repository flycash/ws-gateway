package linkevent

import (
	"context"

	gateway "gitee.com/flycash/ws-gateway"
)

type LinkSelector interface {
	Select(ctx context.Context, msg any) Link
}

type UserIDAsLinkIDSelector struct {
	// 用户 ID => Link 的映射
	// 默认情况下,msg.ReceiverID 就是用户 ID
	links map[int64]gateway.Link
}
