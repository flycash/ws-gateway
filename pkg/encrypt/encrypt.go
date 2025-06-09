package encrypt

import "fmt"

// Encryptor 定义了用于加解密消息体的接口
type Encryptor interface {
	Name() string
	Encrypt(data []byte) ([]byte, error)
	Decrypt(data []byte) ([]byte, error)
}

// Config 加密配置
type Config struct {
	Enabled   bool   `yaml:"enabled"`   // 是否开启加密
	Algorithm string `yaml:"algorithm"` // 加密算法: aes, des, 3des, rsa
	Key       string `yaml:"key"`       // 加密密钥
	IV        string `yaml:"iv"`        // 初始化向量(对于需要的算法)
}

// ErrUnsupportedAlgorithm 不支持的加密算法错误
var ErrUnsupportedAlgorithm = fmt.Errorf("不支持的加密算法")
