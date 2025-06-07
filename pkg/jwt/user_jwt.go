package jwt

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type UserClaims struct {
	UserID int64
	BizID  int64
	jwt.RegisteredClaims
}

// UserJWTKey 因为 JWT Key 不太可能变，所以可以直接写成常量
var UserJWTKey = "tokqi017kqczcj0qvu6zg9zsyjtc0q0b"

type UserToken struct {
	token *Token
}

func NewUserToken(key, issuer string) *UserToken {
	return &UserToken{
		token: New(key, issuer),
	}
}

func (t *UserToken) Decode(tokenString string) (UserClaims, error) {
	mapClaims, err := t.token.Decode(tokenString)
	if err != nil {
		return UserClaims{}, err
	}
	claims := UserClaims{}
	if id, ok := mapClaims["user_id"].(float64); ok {
		claims.UserID = int64(id)
	}
	if bizID, ok := mapClaims["biz_id"].(float64); ok {
		claims.BizID = int64(bizID)
	}
	// 处理标准声明
	if iat, ok := mapClaims["iat"].(float64); ok {
		claims.IssuedAt = jwt.NewNumericDate(time.Unix(int64(iat), 0))
	}
	if exp, ok := mapClaims["exp"].(float64); ok {
		claims.ExpiresAt = jwt.NewNumericDate(time.Unix(int64(exp), 0))
	}
	if iss, ok := mapClaims["iss"].(string); ok {
		claims.Issuer = iss
	}
	return claims, nil
}

// Encode 生成 JWT Token，支持自定义声明和自动添加标准声明
func (t *UserToken) Encode(uc UserClaims) (string, error) {
	// 合并自定义声明和默认声明
	claims := MapClaims{
		"user_id": uc.UserID,
		"biz_id":  uc.BizID,
	}
	if uc.IssuedAt != nil {
		claims["iat"] = uc.IssuedAt.Unix()
	}
	if uc.ExpiresAt != nil {
		claims["exp"] = uc.ExpiresAt.Unix()
	}
	if uc.Issuer != "" {
		claims["iss"] = uc.Issuer
	}

	// 自动处理过期时间
	const day = 24 * time.Hour
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(day).Unix() // 默认24小时过期
	}
	return t.token.Encode(claims)
}
