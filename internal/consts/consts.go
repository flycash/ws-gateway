package consts

import (
	"fmt"

	gateway "gitee.com/flycash/ws-gateway"
)

const (
	sessionCacheKey = "gateway:session:bizID:%d:userID:%d"
)

func SessionCacheKey(sess gateway.Session) string {
	return fmt.Sprintf(sessionCacheKey, sess.BizID, sess.UserID)
}
