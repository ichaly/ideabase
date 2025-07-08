package utl

import (
	"crypto/md5"
	"encoding/hex"
	"hash/crc32"
	"math/rand"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	globalRand = rand.New(rand.NewSource(time.Now().UnixNano()))
	randMu     sync.Mutex
)

// MD5 计算输入字符串的 MD5 哈希值并返回其十六进制表示
func MD5(s string) string {
	d := []byte(s)
	m := md5.New()
	m.Write(d)
	return hex.EncodeToString(m.Sum(nil))
}

// Hash 使用 CRC32 算法计算字符串的哈希值
// 对于长度小于 64 的字符串，会使用固定大小的缓冲区以提高性能
func Hash(key string) int {
	if len(key) < 64 {
		var scratch [64]byte
		copy(scratch[:], key)
		return int(crc32.ChecksumIEEE(scratch[:len(key)]))
	}
	return int(crc32.ChecksumIEEE([]byte(key)))
}

// Root 返回项目的根目录路径
// 通过获取当前文件的运行时信息来定位项目根目录
func Root() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Dir(filepath.Dir(filename))
}

// RandomCode 生成指定长度的随机数字字符串
// width: 要生成的随机字符串长度
// 返回一个只包含数字(0-9)的字符串，如果 width <= 0 则返回空字符串
func RandomCode(width int) string {
	if width <= 0 {
		return ""
	}

	sb := strings.Builder{}
	sb.Grow(width)

	randMu.Lock()
	defer randMu.Unlock()

	for i := 0; i < width; i++ {
		sb.WriteByte('0' + byte(globalRand.Intn(10)))
	}
	return sb.String()
}

// Must 保证函数返回的错误不会为 nil，否则会 panic
func Must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
