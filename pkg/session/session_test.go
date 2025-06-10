//go:build unit

package session_test

import (
	"encoding/json"
	"testing"

	"gitee.com/flycash/ws-gateway/pkg/session"
	"github.com/stretchr/testify/assert"
)

func TestSession(t *testing.T) {
	t.Parallel()
	session1 := session.Session{BizID: 2, UserID: 3}

	marshal, err := json.Marshal(session1)
	assert.NoError(t, err)

	assert.Equal(t, session1.String(), string(marshal))

	var session2 session.Session
	err = json.Unmarshal([]byte(session1.String()), &session2)
	assert.NoError(t, err)

	assert.Equal(t, session1, session2)
}
