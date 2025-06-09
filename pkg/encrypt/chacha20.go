package encrypt

import (
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

type chacha20Encryptor struct {
	aead cipher.AEAD
}

// NewChaCha20Encryptor 创建ChaCha20-Poly1305加密器
func NewChaCha20Encryptor(key string) (Encryptor, error) {
	keyBytes := []byte(key)

	// ChaCha20-Poly1305需要32字节密钥
	if len(keyBytes) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("ChaCha20-Poly1305密钥长度必须为%d字节，当前长度: %d", chacha20poly1305.KeySize, len(keyBytes))
	}

	aead, err := chacha20poly1305.New(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("创建ChaCha20-Poly1305失败: %w", err)
	}

	return &chacha20Encryptor{
		aead: aead,
	}, nil
}

func (c *chacha20Encryptor) Name() string {
	return "chacha20poly1305"
}

func (c *chacha20Encryptor) Encrypt(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}

	// 生成随机nonce
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("生成nonce失败: %w", err)
	}

	// 加密数据，nonce会被添加到密文前面
	ciphertext := c.aead.Seal(nonce, nonce, data, nil)
	return ciphertext, nil
}

func (c *chacha20Encryptor) Decrypt(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}

	nonceSize := c.aead.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("密文长度不足，无法提取nonce")
	}

	// 提取nonce和密文
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]

	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("解密失败: %w", err)
	}

	return plaintext, nil
}
