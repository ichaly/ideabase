package utility

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
	"path"
	"path/filepath"
)

func Md5File(src io.Reader) string {
	dst := md5.New()
	_, _ = io.Copy(dst, src)
	key := hex.EncodeToString(dst.Sum(nil))
	return key
}

func CopyFile(src, dst string) error {
	_ = os.MkdirAll(path.Dir(dst), 0777)
	input, err := os.Open(path.Join(path.Dir(src), path.Base(src)))
	defer input.Close()
	if err != nil {
		return err
	}
	output, err := os.Create(path.Join(path.Dir(dst), path.Base(dst)))
	defer output.Close()
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	if err != nil {
		return err
	}
	return nil
}

func WriteFile(source io.Reader, target string) error {
	_ = os.MkdirAll(filepath.Dir(target), 0777)
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE, 0666)
	if err != nil {
		return err
	}
	defer func(file *os.File) {
		_ = file.Close()
	}(file)
	_, err = io.Copy(file, source)
	return err
}
