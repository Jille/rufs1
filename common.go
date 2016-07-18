package main

import (
	"crypto/sha1"
	"fmt"
	"io"
	"log"
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

func LogRPC(name string, q interface{}, r interface{}, err *error) {
	log.Printf("Handled RPC %s: %+v -> %+v, %v", name, q, r, *err)
}
