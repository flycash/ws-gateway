//go:build unit

package id_test

import (
	"testing"

	"gitee.com/flycash/ws-gateway/pkg/id"
	"github.com/stretchr/testify/assert"
)

func TestNewGenerator(t *testing.T) {
	t.Parallel()

	t.Run("初始化成功", func(t *testing.T) {
		t.Parallel()

		generator, err := id.NewGenerator(0)
		assert.NoError(t, err)
		assert.NotNil(t, generator)
		assert.GreaterOrEqual(t, generator.Generate(), int64(0))
	})
	t.Run("初始化失败", func(t *testing.T) {
		t.Parallel()

		generator, err := id.NewGenerator(1024)
		assert.Error(t, err)
		assert.Nil(t, generator)
	})
}
