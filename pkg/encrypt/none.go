package encrypt

type noneEncryptor struct{}

// NewNoneEncryptor 创建无加密器（明文传输）
func NewNoneEncryptor() Encryptor {
	return &noneEncryptor{}
}

func (n *noneEncryptor) Name() string {
	return "none"
}

func (n *noneEncryptor) Encrypt(data []byte) ([]byte, error) {
	// 不加密，直接返回原数据
	return data, nil
}

func (n *noneEncryptor) Decrypt(data []byte) ([]byte, error) {
	// 不解密，直接返回原数据
	return data, nil
}
