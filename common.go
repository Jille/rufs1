package main

import (
	"log"
	"time"

	"golang.org/x/net/context"
)

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

// Temporary function until we fully migrate to contexts.
func createContext(done <-chan void) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-done
		cancel()
	}()
	return ctx
}
