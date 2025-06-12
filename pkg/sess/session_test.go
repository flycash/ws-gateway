//go:build unit

package sess_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ecodeclub/ekit"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"gitee.com/flycash/ws-gateway/pkg/sess"
	"gitee.com/flycash/ws-gateway/pkg/sess/mocks"
)

func TestRedisSession_UserInfo(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock Lua script execution for session creation
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil))

	session, isNew, err := provider.Provide(context.Background(), userInfo)
	require.NoError(t, err)
	assert.True(t, isNew)

	result := session.UserInfo()
	assert.Equal(t, userInfo, result)
}

func TestRedisSession_Get_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock session creation
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil))

	session, _, err := provider.Provide(context.Background(), userInfo)
	require.NoError(t, err)

	// Mock HGet success
	expectedValue := "test_value"
	mockRedis.EXPECT().HGet(gomock.Any(), "session:bizId:123:userId:456", "test_key").
		Return(redis.NewStringResult(expectedValue, nil))

	result, err := session.Get(context.Background(), "test_key")
	require.NoError(t, err)
	assert.Equal(t, expectedValue, result.Val)
}

func TestRedisSession_Get_NonExistentKey(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock session creation
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil))

	session, _, err := provider.Provide(context.Background(), userInfo)
	require.NoError(t, err)

	// Mock HGet returns redis.Nil (key not found)
	mockRedis.EXPECT().HGet(gomock.Any(), gomock.Any(), "nonexistent_key").
		Return(redis.NewStringResult("", redis.Nil))

	result, err := session.Get(context.Background(), "nonexistent_key")
	require.NoError(t, err)
	assert.Equal(t, ekit.AnyValue{}, result)
}

func TestRedisSession_Get_RedisError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock session creation
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil))

	session, _, err := provider.Provide(context.Background(), userInfo)
	require.NoError(t, err)

	// Mock HGet returns error
	expectedErr := errors.New("redis connection failed")
	mockRedis.EXPECT().HGet(gomock.Any(), gomock.Any(), "test_key").
		Return(redis.NewStringResult("", expectedErr))

	result, err := session.Get(context.Background(), "test_key")
	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)
	assert.Equal(t, ekit.AnyValue{}, result)
}

func TestRedisSession_Set_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock session creation
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil))

	session, _, err := provider.Provide(context.Background(), userInfo)
	require.NoError(t, err)

	// Mock HSet success
	mockRedis.EXPECT().HSet(gomock.Any(), "session:bizId:123:userId:456", "test_key", "test_value").
		Return(redis.NewIntResult(1, nil))

	err = session.Set(context.Background(), "test_key", "test_value")
	assert.NoError(t, err)
}

func TestRedisSession_Set_RedisError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock session creation
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil))

	session, _, err := provider.Provide(context.Background(), userInfo)
	require.NoError(t, err)

	// Mock HSet error
	expectedErr := errors.New("redis write failed")
	mockRedis.EXPECT().HSet(gomock.Any(), gomock.Any(), "test_key", "test_value").
		Return(redis.NewIntResult(0, expectedErr))

	err = session.Set(context.Background(), "test_key", "test_value")
	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

func TestRedisSession_Destroy_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock session creation
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil))

	session, _, err := provider.Provide(context.Background(), userInfo)
	require.NoError(t, err)

	// Mock Del success
	mockRedis.EXPECT().Del(gomock.Any(), "session:bizId:123:userId:456").
		Return(redis.NewIntResult(1, nil))

	err = session.Destroy(context.Background())
	assert.NoError(t, err)
}

func TestRedisSession_Destroy_RedisError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock session creation
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil))

	session, _, err := provider.Provide(context.Background(), userInfo)
	require.NoError(t, err)

	// Mock Del error
	expectedErr := errors.New("redis delete failed")
	mockRedis.EXPECT().Del(gomock.Any(), "session:bizId:123:userId:456").
		Return(redis.NewIntResult(0, expectedErr))

	err = session.Destroy(context.Background())
	assert.Error(t, err)
	assert.ErrorIs(t, err, sess.ErrDestroySessionFailed)
}

func TestRedisSessionProvider_Provide_NewSession(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock Lua script execution returning 1 (new session created)
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
			// Verify the script and arguments
			assert.Equal(t, []string{"session:bizId:123:userId:456"}, keys)
			assert.Equal(t, "loginTime", args[0])
			// Verify timestamp format
			_, err := time.Parse(time.RFC3339Nano, args[1].(string))
			assert.NoError(t, err)

			return redis.NewCmdResult(int64(1), nil)
		})

	session, isNew, err := provider.Provide(context.Background(), userInfo)
	require.NoError(t, err)
	assert.True(t, isNew)
	assert.NotNil(t, session)
	assert.Equal(t, userInfo, session.UserInfo())
}

func TestRedisSessionProvider_Provide_ExistingSession(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock Lua script execution returning 0 (session already exists)
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(0), nil))

	session, isNew, err := provider.Provide(context.Background(), userInfo)
	require.NoError(t, err)
	assert.False(t, isNew)
	assert.NotNil(t, session)
	assert.Equal(t, userInfo, session.UserInfo())
}

func TestRedisSessionProvider_Provide_LuaScriptError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock Lua script execution error
	expectedErr := errors.New("lua script execution failed")
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(nil, expectedErr))

	session, isNew, err := provider.Provide(context.Background(), userInfo)
	assert.Error(t, err)
	assert.False(t, isNew)
	assert.Nil(t, session)
	assert.ErrorIs(t, err, sess.ErrCreateSessionFailed)
}

func TestRedisSessionProvider_Provide_UnexpectedScriptResult(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 123, UserID: 456}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// Mock Lua script execution returning unexpected type
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult("unexpected_string_result", nil))

	session, isNew, err := provider.Provide(context.Background(), userInfo)
	assert.Error(t, err)
	assert.False(t, isNew)
	assert.Nil(t, session)
	assert.ErrorIs(t, err, sess.ErrCreateSessionFailed)
	assert.Contains(t, err.Error(), "未知的脚本结果类型")
}

func TestNewRedisSessionProvider(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)

	provider := sess.NewRedisSessionProvider(mockRedis)
	assert.NotNil(t, provider)
}

func TestSessionLifecycle(t *testing.T) {
	// 完整的 Session 生命周期测试
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRedis := mocks.NewMockCmdable(ctrl)
	userInfo := sess.UserInfo{BizID: 999, UserID: 888}

	provider := sess.NewRedisSessionProvider(mockRedis)

	// 1. 创建新 Session
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil))

	session, isNew, err := provider.Provide(context.Background(), userInfo)
	require.NoError(t, err)
	assert.True(t, isNew)

	// 2. 设置一些数据
	mockRedis.EXPECT().HSet(gomock.Any(), "session:bizId:999:userId:888", "key1", "value1").
		Return(redis.NewIntResult(1, nil))
	mockRedis.EXPECT().HSet(gomock.Any(), "session:bizId:999:userId:888", "key2", "value2").
		Return(redis.NewIntResult(1, nil))

	err = session.Set(context.Background(), "key1", "value1")
	require.NoError(t, err)
	err = session.Set(context.Background(), "key2", "value2")
	require.NoError(t, err)

	// 3. 读取数据
	mockRedis.EXPECT().HGet(gomock.Any(), "session:bizId:999:userId:888", "key1").
		Return(redis.NewStringResult("value1", nil))
	mockRedis.EXPECT().HGet(gomock.Any(), "session:bizId:999:userId:888", "key2").
		Return(redis.NewStringResult("value2", nil))

	val1, err := session.Get(context.Background(), "key1")
	require.NoError(t, err)
	assert.Equal(t, "value1", val1.Val)

	val2, err := session.Get(context.Background(), "key2")
	require.NoError(t, err)
	assert.Equal(t, "value2", val2.Val)

	// 4. 销毁 Session
	mockRedis.EXPECT().Del(gomock.Any(), "session:bizId:999:userId:888").
		Return(redis.NewIntResult(1, nil))

	err = session.Destroy(context.Background())
	require.NoError(t, err)
}

func TestErrorConstants(t *testing.T) {
	// 测试错误常量是否正确定义
	assert.Equal(t, "session已存在", sess.ErrSessionExisted.Error())
	assert.Equal(t, "创建session失败", sess.ErrCreateSessionFailed.Error())
	assert.Equal(t, "销毁session失败", sess.ErrDestroySessionFailed.Error())
}
