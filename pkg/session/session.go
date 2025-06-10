package session

import (
	"fmt"
)

// Session 表示Websocket连接的会话信息
type Session struct {
	BizID  int64 `json:"bizId"`
	UserID int64 `json:"userId"`
}

func (s Session) String() string {
	return fmt.Sprintf(`{"bizId":%d,"userId":%d}`, s.BizID, s.UserID)
}
