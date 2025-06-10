package upgrader

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"

	"gitee.com/flycash/ws-gateway/pkg/jwt"

	"gitee.com/flycash/ws-gateway/internal/consts"
	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/session"
	"github.com/ecodeclub/ecache"
	"github.com/gobwas/httphead"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsflate"
	"github.com/gotomicro/ego/core/elog"
)

var (
	ErrInvalidURI       = errors.New("无效的URI")
	ErrInvalidUserToken = errors.New("无效的UserToken")
	ErrExistedLink      = errors.New("连接已存在")
)

type Upgrader struct {
	//  cache 与Component共享同一个实例,用于获取session中的数据
	cache             ecache.Cache
	token             *jwt.UserToken
	compressionConfig compression.Config
	logger            *elog.Component
}

// New 创建一个升级器
func New(cache ecache.Cache, token *jwt.UserToken, compressionConfig compression.Config) *Upgrader {
	return &Upgrader{
		cache:             cache,
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
	var sess session.Session
	var compressionState *compression.State

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
			s, err := u.getSession(string(uri))
			if err != nil {
				u.logger.Error("获取session失败",
					elog.FieldErr(err),
				)
				return fmt.Errorf("%w", err)
			}

			v := u.cache.Get(context.Background(), consts.SessionCacheKey(s))
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
	if err != nil {
		return session.Session{}, nil, err
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

	return sess, compressionState, nil
}

func (u *Upgrader) getSession(uri string) (session.Session, error) {
	uu, err := url.Parse(uri)
	if err != nil {
		return session.Session{}, ErrInvalidURI
	}

	params := uu.Query()
	token := params.Get("token")
	userClaims, err := u.token.Decode(token)
	if err != nil {
		return session.Session{}, fmt.Errorf("%w: %w", ErrInvalidUserToken, err)
	}
	return session.Session{BizID: userClaims.BizID, UserID: userClaims.UserID}, nil
}
