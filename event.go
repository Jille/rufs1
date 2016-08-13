package main

import (
	"sync"
)

// Event is like https://docs.python.org/2/library/threading.html#event-objects
// It is a boolean that you can block on.
type Event struct {
	ch  chan void
	mtx sync.Mutex
}

func (e *Event) Chan() chan void {
	e.mtx.Lock()
	defer e.mtx.Unlock()
	if e.ch == nil {
		e.ch = make(chan void)
	}
	return e.ch
}

func (e *Event) Set() {
	e.mtx.Lock()
	defer e.mtx.Unlock()
	if e.ch == nil {
		e.ch = make(chan void)
	}
	select {
	case <-e.ch:
	default:
		close(e.ch)
	}
}

func (e *Event) Clear() {
	e.mtx.Lock()
	defer e.mtx.Unlock()
	select {
	case <-e.ch:
		e.ch = make(chan void)
	default:
	}
}

func (e *Event) IsSet() bool {
	e.mtx.Lock()
	defer e.mtx.Unlock()
	if e.ch == nil {
		return false
	}
	select {
	case <-e.ch:
		return true
	default:
		return false
	}
}
