package ioc

import "gitee.com/flycash/ws-gateway/pkg/jwt"

func InitUserToken() *jwt.UserToken {
	return jwt.NewUserToken(jwt.UserJWTKey, "ws-gateway")
}
