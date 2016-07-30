package main

import (
	"crypto/sha1"
	"fmt"
	"io"
	"log"
	"os"
	"time"
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

func LogRPC(name string, q interface{}, r interface{}, err *error) func() {
	start := time.Now()
	done := make(chan void, 1)
	go func() {
		for i := 1; ; i++ {
			select {
			case <-done:
				return
			case <-time.After(time.Duration(i) * time.Second):
				t := time.Now().Sub(start)
				log.Printf("RPC %s still running after %s: %+v", name, t, q)
			}
		}
	}()
	return func() {
		t := time.Now().Sub(start)
		close(done)
		log.Printf("Handled RPC %s in %s: %+v -> %+v, %v", name, t, q, r, *err)
	}
}
