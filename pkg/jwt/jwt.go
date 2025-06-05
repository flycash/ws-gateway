package jwt

import (
	jwtv5 "github.com/golang-jwt/jwt/v5"
)

type UserClaims struct {
	ID    int64
	BizID int64
	jwtv5.RegisteredClaims
}

// JWTKey 因为 JWT Key 不太可能变，所以可以直接写成常量
var JWTKey = []byte("tokqi017kqczcj0qvu6zg9zsyjtc0q0b")
