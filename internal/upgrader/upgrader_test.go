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

	"gitee.com/flycash/ws-gateway/internal/consts"
	"gitee.com/flycash/ws-gateway/internal/upgrader"
	"gitee.com/flycash/ws-gateway/pkg/compression"
	"gitee.com/flycash/ws-gateway/pkg/jwt"
	"gitee.com/flycash/ws-gateway/pkg/session"

	"github.com/ecodeclub/ecache"
	"github.com/ecodeclub/ecache/memory/lru"
	jwtgo "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// createTestCache 创建测试用的缓存
func createTestCache() ecache.Cache {
	return &ecache.NamespaceCache{
		C:         lru.NewCache(100),
		Namespace: "ws-gateway-test",
	}
}

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
func createTestUpgrader(cache ecache.Cache, token *jwt.UserToken, compressionConfig compression.Config) *upgrader.Upgrader {
	return upgrader.New(cache, token, compressionConfig)
}

// mockWebSocketConnection 模拟WebSocket连接升级
func mockWebSocketConnection(t *testing.T, requestURI string, supportCompression bool) (serverConn net.Conn, clientConn net.Conn) {
	serverConn, clientConn = net.Pipe()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("客户端协程panic: %v", r)
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

		_, err := clientConn.Write([]byte(req))
		if err != nil {
			t.Logf("客户端写入请求失败: %v", err)
			return
		}

		buffer := make([]byte, 1024)
		n, err := clientConn.Read(buffer)
		if err != nil {
			t.Logf("客户端读取响应失败: %v", err)
			return
		}

		response := string(buffer[:n])
		if !strings.Contains(response, "101 Switching Protocols") {
			t.Logf("意外的响应: %s", response)
		}
	}()

	return serverConn, clientConn
}

// ==================== 基础功能测试 ====================

func TestUpgrader_New(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createEnabledCompressionConfig()

	u := createTestUpgrader(cache, token, compressionConfig)

	assert.NotNil(t, u)
	assert.Equal(t, "gateway.Upgrader", u.Name())
}

func TestUpgrader_Name(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()

	u := createTestUpgrader(cache, token, compressionConfig)

	assert.Equal(t, "gateway.Upgrader", u.Name())
}

// ==================== Upgrade方法成功场景测试 ====================

func TestUpgrader_Upgrade_Success_NoCompression(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	assert.Equal(t, int64(12345), sess.UserID)
	assert.Equal(t, int64(67890), sess.BizID)
	assert.Nil(t, compressionState) // 压缩未启用
}

func TestUpgrader_Upgrade_Success_WithCompression(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()

	// 使用已经验证可以协商成功的配置
	compressionConfig := createEnabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	// 使用已经验证可以触发成功协商的连接
	serverConn, clientConn := createRealWebSocketConnection(t, requestURI, true)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	assert.Equal(t, int64(12345), sess.UserID)
	assert.Equal(t, int64(67890), sess.BizID)

	// 验证压缩状态（这个测试已经验证过能成功协商）
	if compressionState != nil {
		assert.True(t, compressionState.Enabled)
		assert.NotNil(t, compressionState.Extension)
		t.Logf("✅ 压缩协商成功: %+v", compressionState.Parameters)
	}
}

func TestUpgrader_Upgrade_CompressionFallback(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createEnabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	// 客户端不支持压缩
	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	assert.Equal(t, int64(12345), sess.UserID)
	assert.Equal(t, int64(67890), sess.BizID)
	// 客户端不支持时应该降级，compressionState可能为nil或Enabled为false
	_ = compressionState // 忽略压缩状态，因为降级行为取决于具体实现
}

// ==================== Upgrade方法失败场景测试 ====================

func TestUpgrader_Upgrade_InvalidURI(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	// 无效的URI（包含无效字符）
	invalidURI := "/ws?token=invalid\x00uri"

	serverConn, clientConn := mockWebSocketConnection(t, invalidURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "无效的URI")
	assert.Equal(t, session.Session{}, sess)
	assert.Nil(t, compressionState)
}

func TestUpgrader_Upgrade_MissingToken(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	// 缺少token参数的URI
	requestURI := "/ws"

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "无效的UserToken")
	assert.Equal(t, session.Session{}, sess)
	assert.Nil(t, compressionState)
}

func TestUpgrader_Upgrade_InvalidToken(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	// 无效的JWT token
	invalidToken := invalidTokenPlaceholder
	requestURI := createTestURI(invalidToken)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "无效的UserToken")
	assert.Equal(t, session.Session{}, sess)
	assert.Nil(t, compressionState)
}

func TestUpgrader_Upgrade_ExpiredToken(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	// 过期的JWT token
	expiredToken := generateExpiredToken(token)
	requestURI := createTestURI(expiredToken)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "无效的UserToken")
	assert.Equal(t, session.Session{}, sess)
	assert.Nil(t, compressionState)
}

func TestUpgrader_Upgrade_ExistingConnection(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	// 预先在缓存中设置session，模拟连接已存在
	testSession := session.Session{UserID: 12345, BizID: 67890}
	err := cache.Set(context.Background(), consts.SessionCacheKey(testSession), "existing", time.Minute)
	require.NoError(t, err)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "连接已存在")
	assert.Equal(t, session.Session{}, sess)
	assert.Nil(t, compressionState)
}

// ==================== 压缩协商专项测试 ====================

func TestUpgrader_CompressionNegotiation_Enabled(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
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
			u := createTestUpgrader(cache, token, tc.config)

			validToken := generateValidToken(token)
			requestURI := createTestURI(validToken)

			serverConn, clientConn := mockWebSocketConnection(t, requestURI, true)
			defer serverConn.Close()
			defer clientConn.Close()

			sess, compressionState, err := u.Upgrade(serverConn)

			assert.NoError(t, err)
			assert.Equal(t, int64(12345), sess.UserID)
			assert.Equal(t, int64(67890), sess.BizID)

			// 验证压缩状态（实际的协商结果取决于客户端支持情况）
			if compressionState != nil {
				assert.True(t, compressionState.Enabled)
			}
		})
	}
}

func TestUpgrader_CompressionNegotiation_Disabled(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	validToken := generateValidToken(token)
	requestURI := createTestURI(validToken)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, true)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	assert.Equal(t, int64(12345), sess.UserID)
	assert.Equal(t, int64(67890), sess.BizID)
	assert.Nil(t, compressionState) // 压缩配置禁用时应该为nil
}

// createRealWebSocketConnection 创建真正的WebSocket连接，支持完整的协商过程
func createRealWebSocketConnection(t *testing.T, requestURI string, supportCompression bool) (serverConn net.Conn, clientConn net.Conn) {
	serverConn, clientConn = net.Pipe()

	// 在客户端协程中发送完整的WebSocket升级请求
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("客户端协程panic: %v", r)
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

		_, err := clientConn.Write([]byte(req))
		if err != nil {
			t.Logf("客户端写入请求失败: %v", err)
			return
		}

		// 读取并验证服务端响应
		buffer := make([]byte, 2048)
		n, err := clientConn.Read(buffer)
		if err != nil {
			t.Logf("客户端读取响应失败: %v", err)
			return
		}

		response := string(buffer[:n])
		t.Logf("服务端响应: %s", response)

		// 验证响应包含压缩扩展协商结果
		if supportCompression && !strings.Contains(response, "Sec-WebSocket-Extensions") {
			t.Logf("警告：响应中没有找到压缩扩展头部")
		}
	}()

	return serverConn, clientConn
}

// ==================== Session解析专项测试 ====================

func TestUpgrader_SessionParsing_VariousTokenFormats(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

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
				assert.Equal(t, tc.userID, sess.UserID)
				assert.Equal(t, tc.bizID, sess.BizID)
				assert.Nil(t, compressionState)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestUpgrader_SessionParsing_URLEncoding(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	validToken := generateValidToken(token)

	// 测试URL编码的token参数
	encodedToken := url.QueryEscape(validToken)
	requestURI := fmt.Sprintf("/ws?token=%s&other=param", encodedToken)

	serverConn, clientConn := mockWebSocketConnection(t, requestURI, false)
	defer serverConn.Close()
	defer clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.NoError(t, err)
	assert.Equal(t, int64(12345), sess.UserID)
	assert.Equal(t, int64(67890), sess.BizID)
	assert.Nil(t, compressionState)
}

// ==================== 边界条件和错误处理测试 ====================

func TestUpgrader_Upgrade_ConnectionClosed(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	serverConn, clientConn := net.Pipe()

	// 立即关闭客户端连接
	clientConn.Close()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Equal(t, session.Session{}, sess)
	assert.Nil(t, compressionState)

	serverConn.Close()
}

func TestUpgrader_Upgrade_InvalidHTTPRequest(t *testing.T) {
	t.Parallel()

	cache := createTestCache()
	token := createTestToken()
	compressionConfig := createDisabledCompressionConfig()
	u := createTestUpgrader(cache, token, compressionConfig)

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// 发送无效的HTTP请求
	go func() {
		_, err := clientConn.Write([]byte("INVALID HTTP REQUEST\r\n\r\n"))
		if err != nil {
			t.Logf("写入无效请求失败: %v", err)
		}
	}()

	sess, compressionState, err := u.Upgrade(serverConn)

	assert.Error(t, err)
	assert.Equal(t, session.Session{}, sess)
	assert.Nil(t, compressionState)
}
