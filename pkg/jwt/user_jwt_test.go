package jwt_test

import (
	"log"
	"testing"

	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"github.com/stretchr/testify/assert"
)

func TestUserToken(t *testing.T) {
	t.Parallel()

	token := jwt.NewUserToken(jwt.UserJWTKey, "simple-im")

	userClaims := jwt.UserClaims{BizID: 1, UserID: 1}
	encode, err := token.Encode(userClaims)
	assert.NoError(t, err)

	decode, err := token.Decode(encode)
	assert.NoError(t, err)
	assert.Equal(t, userClaims.UserID, decode.UserID)
	assert.Equal(t, userClaims.BizID, decode.BizID)
	log.Printf("userClaims = %#v\n\tdecode = %#v\n", userClaims, decode)
}
