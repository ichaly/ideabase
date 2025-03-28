package utl

import (
	"crypto/sha256"
)

// SecurePadKey 对加密密钥进行安全填充处理，确保密钥长度符合加密算法要求
// size通常为16、24或32字节，对应AES-128、AES-192和AES-256
func SecurePadKey(key string, size int) string {
	// 如果密钥为空返回空字符串
	if len(key) == 0 {
		return ""
	}

	// 使用SHA-256哈希算法处理密钥
	hasher := sha256.New()
	hasher.Write([]byte(key))
	hash := hasher.Sum(nil)

	// 根据需要的密钥长度截取或填充
	if size > len(hash) {
		// 如果需要更长的密钥，重复使用哈希结果
		result := make([]byte, size)
		copy(result, hash)
		// 循环填充剩余部分
		for i := len(hash); i < size; i++ {
			result[i] = hash[i%len(hash)]
		}
		return string(result)
	}

	// 如果需要较短的密钥，截取哈希结果
	return string(hash[:size])
}
