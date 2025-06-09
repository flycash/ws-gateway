package encrypt

import "fmt"

// NewEncryptor 根据配置创建加密器
func NewEncryptor(config Config) (Encryptor, error) {
	if !config.Enabled {
		return NewNoneEncryptor(), nil
	}
	switch config.Algorithm {
	case "aes":
		if config.Key == "" {
			return nil, fmt.Errorf("AES加密需要提供密钥")
		}
		return NewAESEncryptor(config.Key)
	case "chacha20poly1305", "chacha20":
		if config.Key == "" {
			return nil, fmt.Errorf("ChaCha20-Poly1305加密需要提供密钥")
		}
		return NewChaCha20Encryptor(config.Key)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedAlgorithm, config.Algorithm)
	}
}
