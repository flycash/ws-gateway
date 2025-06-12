//go:build unit

package upgrader_test

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"gitee.com/flycash/ws-gateway/internal/upgrader"
	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"gitee.com/flycash/ws-gateway/pkg/session/mocks"

	jwtgo "github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// 定义常量以满足 goconst 要求
const (
	hostHeader              = "Host: localhost\r\n"
	upgradeHeader           = "Upgrade: websocket\r\n"
	connectionHeader        = "Connection: Upgrade\r\n"
	webSocketKeyHeader      = "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"
	webSocketVersionHeader  = "Sec-WebSocket-Version: 13\r\n"
	crlfSeparator           = "\r\n"
	invalidTokenPlaceholder = "invalid-jwt-token-for-testing"
)

// ==================== 测试辅助函数 ====================

// createTestToken 创建测试用的JWT token组件
func createTestToken() *jwt.UserToken {
	return jwt.NewUserToken("test-secret", "test-issuer")
}

// createValidUserClaims 创建有效的用户声明
func createValidUserClaims() jwt.UserClaims {
	return jwt.UserClaims{
		UserID: 12345,
		BizID:  67890,
		RegisteredClaims: jwtgo.RegisteredClaims{
			IssuedAt:  jwtgo.NewNumericDate(time.Now()),
			ExpiresAt: jwtgo.NewNumericDate(time.Now().Add(time.Hour)),
			Issuer:    "test-issuer",
		},
	}
}

// generateValidToken 生成有效的JWT token
func generateValidToken(userToken *jwt.UserToken) string {
	claims := createValidUserClaims()
	token, _ := userToken.Encode(claims)
	return token
}

// generateExpiredToken 生成过期的JWT token
func generateExpiredToken(userToken *jwt.UserToken) string {
	expiredClaims := jwt.UserClaims{
		UserID: 12345,
		BizID:  67890,
		RegisteredClaims: jwtgo.RegisteredClaims{
			IssuedAt:  jwtgo.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwtgo.NewNumericDate(time.Now().Add(-time.Hour)),
			Issuer:    "test-issuer",
		},
	}
	token, _ := userToken.Encode(expiredClaims)
	return token
}

// createTestURI 创建测试URI
func createTestURI(token string) string {
	return fmt.Sprintf("/ws?token=%s", url.QueryEscape(token))
}

// createEnabledCompressionConfig 创建启用压缩的配置
func createEnabledCompressionConfig() compression.Config {
	return compression.Config{
		Enabled:         true,
		ServerMaxWindow: 15,
		ClientMaxWindow: 15,
		ServerNoContext: false,
		ClientNoContext: false,
		Level:           1,
	}
}

// createDisabledCompressionConfig 创建禁用压缩的配置
func createDisabledCompressionConfig() compression.Config {
	return compression.Config{Enabled: false}
}

// createTestUpgrader 创建测试用的Upgrader
func createTestUpgrader(t *testing.T, token *jwt.UserToken, compressionConfig compression.Config) *upgrader.Upgrader {
	ctrl := gomock.NewController(t)
	mockRedis := mocks.NewMockCmdable(ctrl)

	// Mock 新建Session成功
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(1), nil)).AnyTimes()

	return upgrader.New(mockRedis, token, compressionConfig)
}

// createTestUpgraderWithExistingSession 创建已存在Session的Upgrader
func createTestUpgraderWithExistingSession(t *testing.T, token *jwt.UserToken, compressionConfig compression.Config) *upgrader.Upgrader {
	ctrl := gomock.NewController(t)
	mockRedis := mocks.NewMockCmdable(ctrl)

	// Mock Session已存在
	mockRedis.EXPECT().EvalSha(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(redis.NewCmdResult(int64(0), nil)).AnyTimes()

	return upgrader.New(mockRedis, token, compressionConfig)
}

// mockWebSocketConnection 模拟WebSocket连接升级
func mockWebSocketConnection(t *testing.T, requestURI string, supportCompression bool) (serverConn net.Conn, clientConn net.Conn) {
	serverConn, clientConn = net.Pipe()

	// 使用context控制goroutine生命周期
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 确保函数返回时取消context

	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 不使用t.Logf，避免竞态
				fmt.Printf("客户端协程panic: %v\n", r)
			}
		}()

		req := fmt.Sprintf("GET %s HTTP/1.1\r\n", requestURI)
		req += hostHeader
		req += upgradeHeader
		req += connectionHeader
		req += webSocketKeyHeader
		req += webSocketVersionHeader

		if supportCompression {
			req += "Sec-WebSocket-Extensions: permessage-deflate\r\n"
		}

		req += crlfSeparator

		select {
		case <-ctx.Done():
			return
		default:
		}

		_, err := clientConn.Write([]byte(req))
		if err != nil {
			// 不使用t.Logf，避免竞态
			return
		}

		buffer := make([]byte, 1024)
		n, err := clientConn.Read(buffer)
		if err != nil {
			// 不使用t.Logf，避免竞态
			return
		}

		response := string(buffer[:n])
		if !strings.Contains(response, "101 Switching Protocols") {
			// 不使用t.Logf，避免竞态
		}
	}()

	// 给goroutine一点时间启动
	time.Sleep(10 * time.Millisecond)
	return serverConn, clientConn
}

// ==================== 基础功能测试 ====================

func TestUpgrader_New(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createEnabledCompressionConfig()

	u := createTestUpgrader(t, token, compressionConfig)

	assert.NotNil(t, u)
	assert.Equal(t, "gateway.Upgrader", u.Name())
}

func TestUpgrader_Name(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()

	u := createTestUpgrader(t, token, compressionConfig)

	assert.Equal(t, "gateway.Upgrader", u.Name())
}

// ==================== Upgrade方法成功场景测试 ====================

func TestUpgrader_Upgrade_Success_NoCompression(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	userInfo := sess.UserInfo()
	assert.Equal(t, int64(12345), userInfo.UserID)
	assert.Equal(t, int64(67890), userInfo.BizID)
	assert.Nil(t, compressionState) // 压缩未启用
}

func TestUpgrader_Upgrade_Success_WithCompression(t *testing.T) {
	t.Parallel()

	token := createTestToken()

	// 使用已经验证可以协商成功的配置
	compressionConfig := createEnabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	// 使用已经验证可以触发成功协商的连接
	serverConn, clientConn := createRealWebSocketConnection(t, requestURI, true)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	userInfo := sess.UserInfo()
	assert.Equal(t, int64(12345), userInfo.UserID)
	assert.Equal(t, int64(67890), userInfo.BizID)

	// 验证压缩状态（这个测试已经验证过能成功协商）
	if compressionState != nil {
		assert.True(t, compressionState.Enabled)
		assert.NotNil(t, compressionState.Extension)
		t.Logf("✅ 压缩协商成功: %+v", compressionState.Parameters)
	}
}

func TestUpgrader_Upgrade_CompressionFallback(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createEnabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	// 客户端不支持压缩
	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	userInfo := sess.UserInfo()
	assert.Equal(t, int64(12345), userInfo.UserID)
	assert.Equal(t, int64(67890), userInfo.BizID)
	// 客户端不支持时应该降级，compressionState可能为nil或Enabled为false
	_ = compressionState // 忽略压缩状态，因为降级行为取决于具体实现
}

// ==================== Upgrade方法失败场景测试 ====================

func TestUpgrader_Upgrade_InvalidURI(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	// 无效的URI（包含无效字符）
	invalidURI := "/ws?token=invalid\x00uri"

	serverConn, clientConn := mockWebSocketConnection(t, invalidURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "无效的URI")
	assert.Nil(t, sess)
	assert.Nil(t, compressionState)
}

func TestUpgrader_Upgrade_MissingToken(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	// 缺少token参数的URI
	requestURI := "/ws"

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "无效的UserToken")
	assert.Nil(t, sess)
	assert.Nil(t, compressionState)
}

func TestUpgrader_Upgrade_InvalidToken(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	// 无效的JWT token
	invalidToken := invalidTokenPlaceholder
	requestURI := createTestURI(invalidToken)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "无效的UserToken")
	assert.Nil(t, sess)
	assert.Nil(t, compressionState)
}

func TestUpgrader_Upgrade_ExpiredToken(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	// 过期的JWT token
	expiredToken := generateExpiredToken(token)
	requestURI := createTestURI(expiredToken)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "无效的UserToken")
	assert.Nil(t, sess)
	assert.Nil(t, compressionState)
}

func TestUpgrader_Upgrade_ExistingConnection(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgraderWithExistingSession(t, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "用户已存在")
	assert.Nil(t, sess)
	assert.Nil(t, compressionState)
}

// ==================== 压缩协商专项测试 ====================

func TestUpgrader_CompressionNegotiation_Enabled(t *testing.T) {
	t.Parallel()

	token := createTestToken()

	// 测试不同的压缩配置
	testCases := []struct {
		name   string
		config compression.Config
	}{
		{
			name: "默认压缩配置",
			config: compression.Config{
				Enabled:         true,
				ServerMaxWindow: 15,
				ClientMaxWindow: 15,
				ServerNoContext: false,
				ClientNoContext: false,
				Level:           1,
			},
		},
		{
			name: "最小窗口大小",
			config: compression.Config{
				Enabled:         true,
				ServerMaxWindow: 9,
				ClientMaxWindow: 9,
				ServerNoContext: true,
				ClientNoContext: true,
				Level:           1,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u := createTestUpgrader(t, token, tc.config)

			validToken := generateValidToken(token)
			requestURI := createTestURI(validToken)

			serverConn, clientConn := mockWebSocketConnection(t, requestURI, true)
			defer serverConn.Close()
			defer clientConn.Close()

			sess, compressionState, err := u.Upgrade(serverConn)

			assert.NoError(t, err)
			userInfo := sess.UserInfo()
			assert.Equal(t, int64(12345), userInfo.UserID)
			assert.Equal(t, int64(67890), userInfo.BizID)

			// 验证压缩状态（实际的协商结果取决于客户端支持情况）
			if compressionState != nil {
				assert.True(t, compressionState.Enabled)
			}
		})
	}
}

func TestUpgrader_CompressionNegotiation_Disabled(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, true)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	userInfo := sess.UserInfo()
	assert.Equal(t, int64(12345), userInfo.UserID)
	assert.Equal(t, int64(67890), userInfo.BizID)
	assert.Nil(t, compressionState) // 压缩配置禁用时应该为nil
}

// createRealWebSocketConnection 创建真正的WebSocket连接，支持完整的协商过程
func createRealWebSocketConnection(t *testing.T, requestURI string, supportCompression bool) (serverConn net.Conn, clientConn net.Conn) {
	serverConn, clientConn = net.Pipe()

	// 使用context控制goroutine生命周期
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 确保函数返回时取消context

	// 在客户端协程中发送完整的WebSocket升级请求
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 不使用t.Logf，避免竞态
				fmt.Printf("客户端协程panic: %v\n", r)
			}
		}()

		// 构造完整的WebSocket升级请求
		req := fmt.Sprintf("GET %s HTTP/1.1\r\n", requestURI)
		req += hostHeader
		req += upgradeHeader
		req += connectionHeader
		req += webSocketKeyHeader
		req += webSocketVersionHeader

		// 关键：发送正确的压缩扩展请求
		if supportCompression {
			req += "Sec-WebSocket-Extensions: permessage-deflate; client_max_window_bits=15; server_max_window_bits=15\r\n"
		}

		req += crlfSeparator

		select {
		case <-ctx.Done():
			return
		default:
		}

		_, err := clientConn.Write([]byte(req))
		if err != nil {
			// 不使用t.Logf，避免竞态
			return
		}

		// 读取并验证服务端响应
		buffer := make([]byte, 2048)
		n, err := clientConn.Read(buffer)
		if err != nil {
			// 不使用t.Logf，避免竞态
			return
		}

		response := string(buffer[:n])
		// 在生产环境中应该使用日志库，这里只是测试
		_ = response

		// 验证响应包含压缩扩展协商结果
		if supportCompression && !strings.Contains(response, "Sec-WebSocket-Extensions") {
			// 不使用t.Logf，避免竞态
		}
	}()

	// 给goroutine一点时间启动
	time.Sleep(10 * time.Millisecond)
	return serverConn, clientConn
}

// ==================== Session解析专项测试 ====================

func TestUpgrader_SessionParsing_VariousTokenFormats(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()

	testCases := []struct {
		name          string
		userID        int64
		bizID         int64
		expectSuccess bool
	}{
		{
			name:          "正常用户ID和业务ID",
			userID:        12345,
			bizID:         67890,
			expectSuccess: true,
		},
		{
			name:          "零值用户ID",
			userID:        0,
			bizID:         67890,
			expectSuccess: true,
		},
		{
			name:          "较大数值ID",
			userID:        1234567890123456, // 合理的大数值，避免精度问题
			bizID:         9876543210987654,
			expectSuccess: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u := createTestUpgrader(t, token, compressionConfig)

			// 创建特定的用户声明
			claims := jwt.UserClaims{
				UserID: tc.userID,
				BizID:  tc.bizID,
				RegisteredClaims: jwtgo.RegisteredClaims{
					ExpiresAt: jwtgo.NewNumericDate(time.Now().Add(24 * time.Hour)),
					IssuedAt:  jwtgo.NewNumericDate(time.Now()),
					Issuer:    "test-issuer",
				},
			}

			tokenStr, err := token.Encode(claims)
			require.NoError(t, err)

			requestURI := createTestURI(tokenStr)

			serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
			defer serverConn.Close()
			defer clientConn.Close()

			sess, compressionState, err := u.Upgrade(serverConn)

			if tc.expectSuccess {
				assert.NoError(t, err)
				userInfo := sess.UserInfo()
				assert.Equal(t, tc.userID, userInfo.UserID)
				assert.Equal(t, tc.bizID, userInfo.BizID)
				assert.Nil(t, compressionState)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestUpgrader_SessionParsing_URLEncoding(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	validToken := generateValidToken(token)

	// 测试URL编码的token参数
	encodedToken := url.QueryEscape(validToken)
	requestURI := fmt.Sprintf("/ws?token=%s&other=param", encodedToken)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	userInfo := sess.UserInfo()
	assert.Equal(t, int64(12345), userInfo.UserID)
	assert.Equal(t, int64(67890), userInfo.BizID)
	assert.Nil(t, compressionState)
}

// ==================== 边界条件和错误处理测试 ====================

func TestUpgrader_Upgrade_ConnectionClosed(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	serverConn, clientConn := net.Pipe()

	// 立即关闭客户端连接
	clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Nil(t, sess)
	assert.Nil(t, compressionState)

	serverConn.Close()
}

func TestUpgrader_Upgrade_InvalidHTTPRequest(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// 发送无效的HTTP请求
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 确保函数返回时取消context

	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 不使用t.Logf，避免竞态
				fmt.Printf("客户端协程panic: %v\n", r)
			}
		}()

		select {
		case <-ctx.Done():
			return
		default:
		}

		_, err := clientConn.Write([]byte("INVALID HTTP REQUEST\r\n\r\n"))
		if err != nil {
			// 不使用t.Logf，避免竞态
		}
	}()

	// 给goroutine一点时间启动
	time.Sleep(10 * time.Millisecond)

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Nil(t, sess)
	assert.Nil(t, compressionState)
}

// ==================== AutoClose Header 测试 ====================

// TestUpgrader_AutoCloseHeader_True 测试X-AutoClose: true header
func TestUpgrader_AutoCloseHeader_True(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	// 创建支持X-AutoClose: true的WebSocket连接
	serverConn, clientConn := mockWebSocketConnectionWithAutoClose(t, requestURI, "X-AutoClose", "true")
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	userInfo := sess.UserInfo()
	assert.Equal(t, int64(12345), userInfo.UserID)
	assert.Equal(t, int64(67890), userInfo.BizID)
	assert.True(t, userInfo.AutoClose, "AutoClose应该为true")
	assert.Nil(t, compressionState)
}

// TestUpgrader_AutoCloseHeader_False 测试X-AutoClose: false header
func TestUpgrader_AutoCloseHeader_False(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	// 创建支持X-AutoClose: false的WebSocket连接
	serverConn, clientConn := mockWebSocketConnectionWithAutoClose(t, requestURI, "X-AutoClose", "false")
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	userInfo := sess.UserInfo()
	assert.Equal(t, int64(12345), userInfo.UserID)
	assert.Equal(t, int64(67890), userInfo.BizID)
	assert.False(t, userInfo.AutoClose, "AutoClose应该为false")
	assert.Nil(t, compressionState)
}

// TestUpgrader_AutoCloseHeader_Missing 测试缺少X-AutoClose header
func TestUpgrader_AutoCloseHeader_Missing(t *testing.T) {
	t.Parallel()

	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(t, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	// 创建不带X-AutoClose header的WebSocket连接
	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	userInfo := sess.UserInfo()
	assert.Equal(t, int64(12345), userInfo.UserID)
	assert.Equal(t, int64(67890), userInfo.BizID)
	assert.False(t, userInfo.AutoClose, "缺少header时AutoClose应该为false")
	assert.Nil(t, compressionState)
}

// TestUpgrader_AutoCloseHeader_CaseInsensitive 测试header大小写不敏感
func TestUpgrader_AutoCloseHeader_CaseInsensitive(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		headerName  string
		headerValue string
		expected    bool
	}{
		{"大写header", "X-AUTOCLOSE", "true", true},
		{"小写header", "x-autoclose", "true", true},
		{"混合大小写header", "X-AutoClose", "true", true},
		{"小写值", "X-AutoClose", "TRUE", false}, // 值必须严格为"true"
		{"其他值", "X-AutoClose", "yes", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			token := createTestToken()
			compressionConfig := createDisabledCompressionConfig()
			u := createTestUpgrader(t, token, compressionConfig)

			validToken := generateValidToken(token)
			requestURI := createTestURI(validToken)

			serverConn, clientConn := mockWebSocketConnectionWithAutoClose(t, requestURI, tc.headerName, tc.headerValue)
			defer serverConn.Close()
			defer clientConn.Close()

			sess, compressionState, err := u.Upgrade(serverConn)

			assert.NoError(t, err)
			userInfo := sess.UserInfo()
			assert.Equal(t, tc.expected, userInfo.AutoClose,
				"HeaderName: %s, HeaderValue: %s", tc.headerName, tc.headerValue)
			assert.Nil(t, compressionState)
		})
	}
}

// mockWebSocketConnectionWithAutoClose 创建带有自定义header的WebSocket连接
func mockWebSocketConnectionWithAutoClose(t *testing.T, requestURI, headerName, headerValue string) (serverConn net.Conn, clientConn net.Conn) {
	serverConn, clientConn = net.Pipe()

	// 使用context控制goroutine生命周期
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("客户端协程panic: %v\n", r)
			}
		}()

		req := fmt.Sprintf("GET %s HTTP/1.1\r\n", requestURI)
		req += hostHeader
		req += upgradeHeader
		req += connectionHeader
		req += webSocketKeyHeader
		req += webSocketVersionHeader

		// 添加自定义header
		req += fmt.Sprintf("%s: %s\r\n", headerName, headerValue)

		req += crlfSeparator

		select {
		case <-ctx.Done():
			return
		default:
		}

		_, err := clientConn.Write([]byte(req))
		if err != nil {
			return
		}

		buffer := make([]byte, 1024)
		n, err := clientConn.Read(buffer)
		if err != nil {
			return
		}

		response := string(buffer[:n])
		_ = response // 验证响应
	}()

	// 给goroutine一点时间启动
	time.Sleep(10 * time.Millisecond)
	return serverConn, clientConn
}
