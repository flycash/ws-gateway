package upgrader

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"github.com/redis/go-redis/v9"

	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"github.com/gobwas/httphead"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gotomicro/ego/core/elog"
)

var (
	ErrInvalidURI       = errors.New("无效的URI")
	ErrInvalidUserToken = errors.New("无效的UserToken")
	ErrExistedUser      = errors.New("用户已存在")
)

type Upgrader struct {
	rdb               redis.Cmdable
	token             *jwt.UserToken
	compressionConfig compression.Config
	logger            *elog.Component
}

// New 创建一个升级器
func New(rdb redis.Cmdable, token *jwt.UserToken, compressionConfig compression.Config) *Upgrader {
	return &Upgrader{
		rdb:               rdb,
		token:             token,
		compressionConfig: compressionConfig,
		logger:            elog.EgoLogger.With(elog.FieldComponent("Upgrader")),
	}
}

func (u *Upgrader) Name() string {
	return "gateway.Upgrader"
}

// Upgrade 升级连接并支持压缩协商
func (u *Upgrader) Upgrade(conn net.Conn) (session.Session, *compression.State, error) {
	var ss session.Session
	var compressionState *compression.State
	var autoClose bool
	var userInfo session.UserInfo

	// 只有配置启用时才创建压缩扩展
	var ext *wsflate.Extension
	if u.compressionConfig.Enabled {
		params := u.compressionConfig.ToParameters()
		ext = &wsflate.Extension{Parameters: params}

		u.logger.Info("压缩功能已启用",
			elog.Any("parameters", params))
	}

	upgrader := ws.Upgrader{
		Negotiate: func(opt httphead.Option) (httphead.Option, error) {
			if ext != nil {
				return ext.Negotiate(opt)
			}
			return httphead.Option{}, nil
		},
		OnRequest: func(uri []byte) error {
			var err error
			userInfo, err = u.getUserInfo(string(uri))
			if err != nil {
				u.logger.Error("获取用户信息失败",
					elog.FieldErr(err),
				)
				return fmt.Errorf("%w", err)
			}
			return nil
		},
		OnHeader: func(key, value []byte) error {
			// 解析 X-AutoClose header (大小写不敏感)
			if strings.EqualFold(string(key), "X-AutoClose") {
				autoClose = string(value) == "true"
				u.logger.Debug("解析到AutoClose header",
					elog.String("key", string(key)),
					elog.String("value", string(value)),
					elog.Any("autoClose", autoClose))
			}
			return nil
		},
		OnBeforeUpgrade: func() (ws.HandshakeHeader, error) {
			// 在升级前设置autoClose并创建session
			userInfo.AutoClose = autoClose

			provider := session.NewRedisSessionBuilder(u.rdb)
			s, isNew, err := provider.Build(context.Background(), userInfo)
			if err != nil {
				return nil, fmt.Errorf("%w", err)
			}
			if !isNew {
				err = ErrExistedUser
				u.logger.Error("用户已存在",
					elog.FieldErr(err),
				)
				return nil, fmt.Errorf("%w", err)
			}
			ss = s
			return ws.HandshakeHeaderString(""), nil
		},
	}

	_, err := upgrader.Upgrade(conn)
	if err != nil {
		return nil, nil, err
	}

	// 检查压缩协商结果
	if ext != nil {
		if params, accepted := ext.Accepted(); accepted {
			compressionState = &compression.State{
				Enabled:    true,
				Extension:  ext,
				Parameters: params,
			}
			u.logger.Info("压缩协商成功",
				elog.Any("negotiated_params", params))
		} else {
			u.logger.Warn("压缩协商失败，降级到无压缩模式")
		}
	}
	return ss, compressionState, nil
}

func (u *Upgrader) getUserInfo(uri string) (session.UserInfo, error) {
	uu, err := url.Parse(uri)
	if err != nil {
		return session.UserInfo{}, ErrInvalidURI
	}

	params := uu.Query()
	token := params.Get("token")
	userClaims, err := u.token.Decode(token)
	if err != nil {
		return session.UserInfo{}, fmt.Errorf("%w: %w", ErrInvalidUserToken, err)
	}

	return session.UserInfo{
		BizID:  userClaims.BizID,
		UserID: userClaims.UserID,
		// AutoClose将在OnHeader回调中设置
	}, nil
}
