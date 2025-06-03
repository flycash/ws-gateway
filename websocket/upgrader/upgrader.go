package upgrader

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"

	gateway "gitee.com/flycash/ws-gateway"
	channelv1 "gitee.com/flycash/ws-gateway/api/proto/gen/channel/v1"
	jwt "gitee.com/flycash/ws-gateway/pkg/jwt"
	"gitee.com/flycash/ws-gateway/websocket/consts"
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
	localCache           ecache.Cache
	channelServiceClient channelv1.ChannelServiceClient
	logger               *elog.Component
}

// New 创建一个升级器
func New(cache ecache.Cache, channelServiceClient channelv1.ChannelServiceClient) gateway.Upgrader {
	return &upgrader{
		localCache:           cache,
		channelServiceClient: channelServiceClient,
		logger:               elog.EgoLogger.With(elog.FieldComponent("WebsocketUpgrader")),
	}
}

func (u *upgrader) Name() string {
	return "websocket.upgrader"
}

func (u *upgrader) Upgrade(conn net.Conn) (gateway.Session, error) {
	var session gateway.Session

	upgrader := ws.Upgrader{
		OnRequest: func(uri []byte) error {
			sess, err := u.checkParams(string(uri))
			if err != nil {
				u.logger.Error("upgrader", elog.String("step", "检查参数合法性"), elog.FieldErr(err))
				return fmt.Errorf("%w", err)
			}

			// todo: 当允许多端登录的时候,要拿用户端信息过来验证每个端是否已有连接建立
			err = u.isAllowedToUpgrade(sess)
			if err != nil {
				u.logger.Error("upgrader", elog.String("step", "是否允许升级连接"), elog.FieldErr(err))
				return fmt.Errorf("%w", err)
			}

			session = sess
			return nil
		},
	}

	_, err := upgrader.Upgrade(conn)
	if err != nil {
		return gateway.Session{}, err
	}
	return session, nil
}

func (u *upgrader) checkParams(uri string) (gateway.Session, error) {
	uu, err := url.Parse(uri)
	if err != nil {
		return gateway.Session{}, ErrInvalidURI
	}

	queryParams := uu.Query()

	token := queryParams.Get("token")
	user, err := u.parseToken(token)
	if err != nil {
		return gateway.Session{}, err
	}

	return gateway.Session{UID: user.ID}, nil
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

func (u *upgrader) isAllowedToUpgrade(session gateway.Session) error {
	uid := fmt.Sprintf("%d", session.UID)
	if v := u.localCache.Get(context.Background(), consts.UserWebSocketConnIDCacheKey(uid)); v.Err == nil {
		return ErrExistedLink
	}
	return nil
}
