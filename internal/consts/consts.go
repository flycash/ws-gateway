package consts

import (
	"fmt"

	"gitee.com/flycash/ws-gateway/pkg/session"
)

const (
	sessionCacheKey = "gateway:session:bizID:%d:userID:%d"
)

func SessionCacheKey(sess session.Session) string {
	return fmt.Sprintf(sessionCacheKey, sess.BizID, sess.UserID)
}
