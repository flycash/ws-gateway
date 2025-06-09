//go:build unit

package encrypt_test

import (
	"testing"

	"gitee.com/flycash/ws-gateway/pkg/encrypt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

func TestAESEncryptorTestSuite(t *testing.T) {
	t.Parallel()

	// AES-256密钥（32字节）
	key := "1234567890abcdef1234567890abcdef"
	encryptor, err := encrypt.NewAESEncryptor(key)
	assert.NoError(t, err)
	assert.Equal(t, "aes", encryptor.Name())

	suite.Run(t, &EncryptorSuite{encryptor: encryptor})
}

func TestChaCha20EncryptorTestSuite(t *testing.T) {
	t.Parallel()

	// ChaCha20-Poly1305密钥（32字节）
	key := "1234567890abcdef1234567890abcdef"
	encryptor, err := encrypt.NewChaCha20Encryptor(key)
	assert.NoError(t, err)
	assert.Equal(t, "chacha20poly1305", encryptor.Name())

	suite.Run(t, &EncryptorSuite{encryptor: encryptor})
}

func TestNoneEncryptorTestSuite(t *testing.T) {
	t.Parallel()

	encryptor := encrypt.NewNoneEncryptor()
	assert.Equal(t, "none", encryptor.Name())

	suite.Run(t, &EncryptorSuite{encryptor: encryptor})
}

type EncryptorSuite struct {
	suite.Suite
	encryptor encrypt.Encryptor
}

func (s *EncryptorSuite) TestEncryptAndDecrypt() {
	t := s.T()

	testCases := []struct {
		name string
		data []byte
	}{
		{
			name: "普通文本",
			data: []byte("Hello, World!"),
		},
		{
			name: "中文文本",
			data: []byte("你好，世界！"),
		},
		{
			name: "JSON数据",
			data: []byte(`{"message": "test", "id": 123}`),
		},
		{
			name: "空数据",
			data: []byte{},
		},
		{
			name: "二进制数据",
			data: []byte{0x00, 0x01, 0x02, 0x03, 0xFF, 0xFE, 0xFD},
		},
		{
			name: "长文本",
			data: []byte("Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua."),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// 加密
			encrypted, err := s.encryptor.Encrypt(tc.data)
			assert.NoError(t, err)

			// 对于none加密器，加密后应该与原数据相同
			if s.encryptor.Name() == "none" {
				assert.Equal(t, tc.data, encrypted)
			} else if len(tc.data) > 0 {
				// 对于真正的加密器，加密后应该与原数据不同（除非是空数据）
				assert.NotEqual(t, tc.data, encrypted)
			}

			// 解密
			decrypted, err := s.encryptor.Decrypt(encrypted)
			assert.NoError(t, err)

			// 解密后应该与原数据相同
			assert.Equal(t, tc.data, decrypted)
		})
	}
}

func TestFactoryCreation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		config     encrypt.Config
		expectErr  bool
		expectName string
	}{
		{
			name: "None加密器",
			config: encrypt.Config{
				Enabled:   false,
				Algorithm: "none",
			},
			expectErr:  false,
			expectName: "none",
		},
		{
			name: "AES加密器",
			config: encrypt.Config{
				Enabled:   true,
				Algorithm: "aes",
				Key:       "1234567890abcdef1234567890abcdef",
			},
			expectErr:  false,
			expectName: "aes",
		},
		{
			name: "ChaCha20-Poly1305加密器",
			config: encrypt.Config{
				Enabled:   true,
				Algorithm: "chacha20poly1305",
				Key:       "1234567890abcdef1234567890abcdef",
			},
			expectErr:  false,
			expectName: "chacha20poly1305",
		},
		{
			name: "无效算法",
			config: encrypt.Config{
				Enabled:   true,
				Algorithm: "invalid",
				Key:       "somekey",
			},
			expectErr: true,
		},
		{
			name: "AES无密钥",
			config: encrypt.Config{
				Enabled:   true,
				Algorithm: "aes",
				Key:       "",
			},
			expectErr: true,
		},
		{
			name: "AES密钥长度不正确",
			config: encrypt.Config{
				Enabled:   true,
				Algorithm: "aes",
				Key:       "short",
			},
			expectErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			encryptor, err := encrypt.NewEncryptor(tc.config)

			if tc.expectErr {
				assert.Error(t, err)
				assert.Nil(t, encryptor)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, encryptor)
				assert.Equal(t, tc.expectName, encryptor.Name())
			}
		})
	}
}
