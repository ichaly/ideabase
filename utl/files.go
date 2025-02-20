package utl

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
)

// Md5File 计算文件内容的MD5值
func Md5File(src io.Reader) string {
	dst := md5.New()
	_, _ = io.Copy(dst, src)
	return hex.EncodeToString(dst.Sum(nil))
}

func CopyFile(src, dst string) error {
	if src == "" || dst == "" {
		return fmt.Errorf("empty source or destination path")
	}

	// 创建目标目录
	if err := os.MkdirAll(path.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// 打开源文件
	input, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer input.Close()

	// 创建目标文件（如果文件已存在则返回错误）
	output, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}
	defer func() {
		closeErr := output.Close()
		if err == nil {
			err = closeErr
		}
	}()

	// 复制文件内容
	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf("copy content: %w", err)
	}

	return nil
}

// WriteFile 将数据写入文件
func WriteFile(source io.Reader, target string) error {
	if source == nil {
		return fmt.Errorf("nil source reader")
	}

	if target == "" {
		return fmt.Errorf("empty target path")
	}

	// 创建目标目录
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// 创建或覆盖目标文件
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	defer func() {
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
	}()

	// 写入数据
	if _, err := io.Copy(file, source); err != nil {
		return fmt.Errorf("write data: %w", err)
	}

	return nil
}
