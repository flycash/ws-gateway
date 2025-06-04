package upgrader

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"

	"gitee.com/flycash/ws-gateway/internal/pkg/jwt"

	gateway "gitee.com/flycash/ws-gateway"
	"gitee.com/flycash/ws-gateway/internal/consts"
	"github.com/ecodeclub/ecache"
	"github.com/gobwas/ws"
	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gotomicro/ego/core/elog"
)

var (
	_                    gateway.Upgrader = &upgrader{}
	ErrInvalidURI                         = errors.New("无效的URI")
	ErrInvalidCIDOrToken                  = errors.New("无效的群聊ID或Token")
	ErrExistedLink                        = errors.New("连接已存在")
)

type upgrader struct {
	//  localCache 与Component共享同一个实例,用于获取session中的数据
	localCache ecache.Cache
	logger     *elog.Component
}

// New 创建一个升级器
func New(cache ecache.Cache) gateway.Upgrader {
	return &upgrader{
		localCache: cache,
		logger:     elog.EgoLogger.With(elog.FieldComponent("Upgrader")),
	}
}

func (u *upgrader) Name() string {
	return "gateway.upgrader"
}

func (u *upgrader) Upgrade(conn net.Conn) (gateway.Session, error) {
	var session gateway.Session

	upgrader := ws.Upgrader{
		OnRequest: func(uri []byte) error {
			sess, err := u.getSession(string(uri))
			if err != nil {
				u.logger.Error("获取session失败",
					elog.FieldErr(err),
				)
				return fmt.Errorf("%w", err)
			}

			v := u.localCache.Get(context.Background(), consts.SessionCacheKey(session))
			if !v.KeyNotFound() {
				err = ErrExistedLink
				u.logger.Error("Link已存在",
					elog.FieldErr(err),
				)
				return fmt.Errorf("%w", err)
			}

			session = sess
			return nil
		},
	}

	_, err := upgrader.Upgrade(conn)
	return session, err
}

func (u *upgrader) getSession(uri string) (gateway.Session, error) {
	uu, err := url.Parse(uri)
	if err != nil {
		return gateway.Session{}, ErrInvalidURI
	}

	params := uu.Query()
	token := params.Get("token")
	userClaims, err := u.parseToken(token)
	if err != nil {
		return gateway.Session{}, err
	}
	return gateway.Session{BizID: userClaims.BizID, UserID: userClaims.ID}, nil
}

func (u *upgrader) parseToken(token string) (*jwt.UserClaims, error) {
	user := &jwt.UserClaims{}
	jwtToken, err := jwtv5.ParseWithClaims(token, user, func(_ *jwtv5.Token) (interface{}, error) {
		return jwt.JWTKey, nil
	})
	if err != nil || jwtToken == nil || !jwtToken.Valid || user.ID < 1 {
		return nil, ErrInvalidCIDOrToken
	}
	return user, nil
}
