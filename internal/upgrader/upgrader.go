package upgrader

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"

	"gitee.com/flycash/ws-gateway/pkg/jwt"

	"gitee.com/flycash/ws-gateway/internal/consts"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"github.com/ecodeclub/ecache"
	"github.com/gobwas/ws"
	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gotomicro/ego/core/elog"
)

var (
	ErrInvalidURI        = errors.New("无效的URI")
	ErrInvalidCIDOrToken = errors.New("无效的群聊ID或Token")
	ErrExistedLink       = errors.New("连接已存在")
)

type Upgrader struct {
	//  localCache 与Component共享同一个实例,用于获取session中的数据
	localCache ecache.Cache
	logger     *elog.Component
}

// New 创建一个升级器
func New(cache ecache.Cache) *Upgrader {
	return &Upgrader{
		localCache: cache,
		logger:     elog.EgoLogger.With(elog.FieldComponent("Upgrader")),
	}
}

func (u *Upgrader) Name() string {
	return "gateway.Upgrader"
}

func (u *Upgrader) Upgrade(conn net.Conn) (session.Session, error) {
	var sess session.Session

	upgrader := ws.Upgrader{
		OnRequest: func(uri []byte) error {
			s, err := u.getSession(string(uri))
			if err != nil {
				u.logger.Error("获取session失败",
					elog.FieldErr(err),
				)
				return fmt.Errorf("%w", err)
			}

			v := u.localCache.Get(context.Background(), consts.SessionCacheKey(s))
			if !v.KeyNotFound() {
				err = ErrExistedLink
				u.logger.Error("Link已存在",
					elog.FieldErr(err),
				)
				return fmt.Errorf("%w", err)
			}

			sess = s
			return nil
		},
	}

	_, err := upgrader.Upgrade(conn)
	return sess, err
}

func (u *Upgrader) getSession(uri string) (session.Session, error) {
	uu, err := url.Parse(uri)
	if err != nil {
		return session.Session{}, ErrInvalidURI
	}

	params := uu.Query()
	token := params.Get("token")
	userClaims, err := u.parseToken(token)
	if err != nil {
		return session.Session{}, err
	}
	return session.Session{BizID: userClaims.BizID, UserID: userClaims.ID}, nil
}

func (u *Upgrader) parseToken(token string) (*jwt.UserClaims, error) {
	user := &jwt.UserClaims{}
	jwtToken, err := jwtv5.ParseWithClaims(token, user, func(_ *jwtv5.Token) (interface{}, error) {
		return jwt.JWTKey, nil
	})
	if err != nil || jwtToken == nil || !jwtToken.Valid || user.ID < 1 {
		return nil, ErrInvalidCIDOrToken
	}
	return user, nil
}
