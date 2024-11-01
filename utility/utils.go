package utility

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"math/rand"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filename))
}

func RandomCode(width int) string {
	numeric := [10]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	r := len(numeric)
	rand.New(rand.NewSource(time.Now().UnixNano()))

	var sb strings.Builder
	for i := 0; i < width; i++ {
		_, _ = fmt.Fprintf(&sb, "%d", numeric[rand.Intn(r)])
	}
	return sb.String()
}
