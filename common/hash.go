package common

import (
	"crypto/sha1"
	"fmt"
	"io"
	"os"
)

func HashFile(fn string) (string, error) {
	h := sha1.New()
	fh, err := os.Open(fn)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(h, fh)
	fh.Close()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
