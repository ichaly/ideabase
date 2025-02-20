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

func MD5(s string) string {
	d := []byte(s)
	m := md5.New()
	m.Write(d)
	return hex.EncodeToString(m.Sum(nil))
}

func Hash(key string) int {
	if len(key) < 64 {
		var scratch [64]byte
		copy(scratch[:], key)
		return int(crc32.ChecksumIEEE(scratch[:len(key)]))
	}
	return int(crc32.ChecksumIEEE([]byte(key)))
}

func Root() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Dir(filepath.Dir(filename))
}

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
