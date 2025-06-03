package consts

import "fmt"

const (
	userWebSocketConnIDCacheKey = "user:%s:ws"
	linkInfoCacheKey            = "link:%s"
)

func UserWebSocketConnIDCacheKey(uid string) string {
	return fmt.Sprintf(userWebSocketConnIDCacheKey, uid)
}

func LinkInfoCacheKey(linkID string) string {
	return fmt.Sprintf(linkInfoCacheKey, linkID)
}
