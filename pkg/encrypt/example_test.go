//go:build unit

package encrypt_test

import (
	"fmt"
	"log"

	"gitee.com/flycash/ws-gateway/pkg/encrypt"
)

func ExampleEncryptor() {
	// 配置AES加密
	config := encrypt.Config{
		Enabled:   true,
		Algorithm: "aes",
		Key:       "1234567890abcdef1234567890abcdef", // 32字节AES-256密钥
	}

	// 创建加密器
	encryptor, err := encrypt.NewEncryptor(config)
	if err != nil {
		log.Fatal(err)
	}

	// 原始数据
	data := []byte("Hello, World! 这是需要加密的数据")

	// 加密
	encrypted, err := encryptor.Encrypt(data)
	if err != nil {
		log.Fatal(err)
	}

	// 解密
	decrypted, err := encryptor.Decrypt(encrypted)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("加密器: %s\n", encryptor.Name())
	fmt.Printf("原始数据: %s\n", string(data))
	fmt.Printf("加密后长度: %d 字节\n", len(encrypted))
	fmt.Printf("解密后数据: %s\n", string(decrypted))
	fmt.Printf("数据完整性: %t\n", string(data) == string(decrypted))

	// Output:
	// 加密器: aes
	// 原始数据: Hello, World! 这是需要加密的数据
	// 加密后长度: 69 字节
	// 解密后数据: Hello, World! 这是需要加密的数据
	// 数据完整性: true
}
