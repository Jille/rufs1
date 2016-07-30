// +build !windows,!nofuse !noftp

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type FrontendLib struct {
	server      *Server
	master      *RUFSMasterClient
	fetcher     *Fetcher
	cache       map[string]*GetDirReply
	cacheExpiry map[string]int
	cacheMtx    sync.Mutex
}

func (f *FrontendLib) cachePurger(done <-chan void) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		f.cacheMtx.Lock()
		for dn, exp := range f.cacheExpiry {
			if exp <= 1 {
				delete(f.cacheExpiry, dn)
				delete(f.cache, dn)
			} else {
				f.cacheExpiry[dn]--
			}
		}
		f.cacheMtx.Unlock()
	}
}

func (f *FrontendLib) purgeCacheEntry(dn string) {
	f.cacheMtx.Lock()
	defer f.cacheMtx.Unlock()
	if _, ok := f.cache[dn]; ok {
		delete(f.cacheExpiry, dn)
		delete(f.cache, dn)
	}
}

func (f *FrontendLib) GetDirCached(dn string) (*GetDirReply, error) {
	dn = strings.Trim(dn, "/")
	f.cacheMtx.Lock()
	if gdr, ok := f.cache[dn]; ok {
		f.cacheMtx.Unlock()
		return gdr, nil
	}
	f.cacheMtx.Unlock()
	ret, err := f.master.GetDir(dn)
	if err != nil {
		return nil, err
	}
	f.cacheMtx.Lock()
	f.cache[dn] = ret
	f.cacheExpiry[dn] = 3
	f.cacheMtx.Unlock()
	return ret, nil
}

func (f *FrontendLib) GetFileInfo(fn string) (fi FileInfo, retErr error) {
	dn, fn := filepath.Split(fn)
	ret, err := f.GetDirCached(dn)
	if err != nil {
		return fi, err
	}
	if fi, found := ret.Files[fn]; found {
		return fi, nil
	}
	return fi, errors.New("file not found")
}
