package jwt

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrTokenExpired           = jwt.ErrTokenExpired
	ErrTokenSignatureInvalid  = jwt.ErrTokenSignatureInvalid
	ErrDecodeJWTTokenFailed   = errors.New("JWT令牌解析失败")
	ErrInvalidJWTToken        = errors.New("无效的令牌")
	ErrSupportedSignAlgorithm = errors.New("不支持的签名算法")
)

type MapClaims jwt.MapClaims

type Token struct {
	key    string
	issuer string
}

func New(key, issuer string) *Token {
	return &Token{
		key:    key,
		issuer: issuer,
	}
}

func (t *Token) Decode(tokenString string) (MapClaims, error) {
	// 去除可能的 Bearer 前缀（兼容不同客户端实现）
	tokenString = strings.TrimPrefix(tokenString, "Bearer ")

	// 解析 Token
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%w: %v", ErrSupportedSignAlgorithm, token.Header["alg"])
		}
		return []byte(t.key), nil
	})
	// 错误处理
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecodeJWTTokenFailed, err)
	}

	// 验证 Token 有效性
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		return MapClaims(claims), nil
	}
	return nil, fmt.Errorf("%w", ErrInvalidJWTToken)
}

// Encode 生成 JWT Token，支持自定义声明和自动添加标准声明
func (t *Token) Encode(customClaims MapClaims) (string, error) {
	// 合并自定义声明和默认声明
	claims := jwt.MapClaims{
		"iat": time.Now().Unix(),
		"iss": t.issuer,
	}

	// 合并用户自定义声明（覆盖默认声明）
	for k, v := range customClaims {
		claims[k] = v
	}
	// 自动处理过期时间
	const day = 24 * time.Hour
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(day).Unix() // 默认24小时过期
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(t.key))
}
