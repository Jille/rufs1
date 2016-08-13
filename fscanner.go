package main

import (
	"compress/gzip"
	"encoding/gob"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

var (
	disableFsNotify = flag.Bool("disable_fsnotify", false, "disable fsnotify (portable inotify) and fall back to regular sweeps")
)

func init() {
	registerServerModule(func(s *Server) (module, error) {
		if *share != "" {
			return newFScanner(*share, s)
		}
		return nil, nil
	})
}

type FScanner struct {
	share           string
	server          *Server
	rpcCh           chan SetFileRequest
	notifications   chan fsnotify.Event
	fileChanged     chan string
	watcher         *fsnotify.Watcher
	updateHashCache bool
	fullResync      Event
}

func newFScanner(share string, s *Server) (*FScanner, error) {
	return &FScanner{
		share:         share,
		server:        s,
		rpcCh:         make(chan SetFileRequest),
		notifications: make(chan fsnotify.Event),
		fileChanged:   make(chan string),
	}, nil
}

func (f *FScanner) Setup() (retErr error) {
	if !*disableFsNotify {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			log.Printf("could not initialize fsnotify: %v", err)
		} else {
			f.watcher = w
		}
	}
	return nil
}

func (f *FScanner) Run(done <-chan void) (retErr error) {
	f.server.master.AppendReconnectCallback(func(c *RUFSMasterClient) error {
		f.fullResync.Set()
		return nil
	})
	defer close(f.rpcCh)
	defer close(f.notifications)
	for i := 0; 4 > i; i++ {
		go f.rpcThread()
	}
	initwT := make(chan time.Time)
	var wT <-chan time.Time = initwT
	go func() {
		cacheSeed, err := f.readHashCache()
		if err != nil {
			log.Printf("Couldn't load hashcache.dat: %v", err)
		} else {
			f.walk("", true, cacheSeed)
		}
		go f.hashCacheFlusher(done)
		initwT <- time.Now()
	}()
	var fsNotifyEvents chan fsnotify.Event
	var fsNotifyErrs chan error
	if f.watcher != nil {
		fsNotifyEvents = f.watcher.Events
		fsNotifyErrs = f.watcher.Errors
		go f.notificationHandler()
	}
	for {
		select {
		case event := <-fsNotifyEvents:
			log.Println("event:", event)
			f.notifications <- event
		case err := <-fsNotifyErrs:
			log.Println("fsnotify error:", err)
			f.watcher.Close()
			f.watcher = nil
			fsNotifyEvents = nil
			fsNotifyErrs = nil
			wT = time.After(time.Minute)
		case fn := <-f.fileChanged:
			f.walk(fn, false, nil)
		case <-wT:
			f.walk("", false, nil)
			if f.watcher == nil {
				wT = time.After(time.Minute)
			} else {
				wT = time.After(time.Hour)
			}
		case <-f.fullResync.Chan():
			f.fullResync.Clear()
			fileCacheMtx.Lock()
			queue := make([]SetFileRequest, 0, len(fileCache))
			for fn, info := range fileCache {
				info := info // capture loop variable
				queue = append(queue, SetFileRequest{
					Path: fn,
					Info: &info,
				})
			}
			fileCacheMtx.Unlock()
			for _, req := range queue {
				f.rpcCh <- req
			}
			wT = time.After(time.Nanosecond)
		case <-done:
			return nil
		}
	}
}

func (f *FScanner) rpcThread() {
	for req := range f.rpcCh {
		if err := retryBackoff(func() error { return f.server.master.SetFile(req) }); err != nil {
			log.Printf("SetFile(%+v) failed: %v", req, err)
			continue
		}
		fileCacheMtx.Lock()
		if f, ok := fileCache[req.Path]; ok {
			delete(hashToPath[f.Hash], req.Path)
		}
		if req.Info == nil {
			delete(fileCache, req.Path)
		} else {
			fileCache[req.Path] = *req.Info
			if _, ok := hashToPath[req.Info.Hash]; !ok {
				hashToPath[req.Info.Hash] = map[string]void{}
			}
			hashToPath[req.Info.Hash][req.Path] = void{}
		}
		f.updateHashCache = true
		fileCacheMtx.Unlock()
	}
}

func (f *FScanner) walk(dir string, fastPass bool, cacheSeed map[string]FileInfo) {
	missing := map[string]bool{}
	for fn := range fileCache {
		if strings.HasPrefix(fn, dir) {
			missing[fn] = true
		}
	}
	filepath.Walk(filepath.Join(f.share, dir), func(path string, info os.FileInfo, err error) error {
		if f.fullResync.IsSet() {
			return errors.New("walk aborted for fullResync")
		}
		if err != nil {
			return nil
		}
		fmt.Printf("%s (%v, %v)\n", path, info, err)
		rel, err := filepath.Rel(f.share, path)
		if err != nil {
			panic(err)
		}
		if base := filepath.Base(rel); base[0] == '.' && base != "." && base != ".." {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode()&os.ModeType == os.ModeDir {
			if f.watcher != nil {
				f.watcher.Add(path)
			}
			return nil
		}
		if info.Mode()&os.ModeType > 0 {
			return nil
		}
		fileCacheMtx.Lock()
		if f, ok := fileCache[rel]; ok && f.Size == info.Size() && f.Mtime == info.ModTime() {
			fileCacheMtx.Unlock()
			delete(missing, rel)
			return nil
		}
		fileCacheMtx.Unlock()
		var hash string
		if f, ok := cacheSeed[rel]; ok && f.Size == info.Size() && f.Mtime == info.ModTime() {
			hash = f.Hash
		} else {
			if fastPass {
				return nil
			}
			hash, err = HashFile(path)
			if err != nil {
				return nil
			}
		}
		delete(missing, rel)
		f.rpcCh <- SetFileRequest{
			Path: rel,
			Info: &FileInfo{
				Hash:  hash,
				Size:  info.Size(),
				Mtime: info.ModTime(),
			},
		}
		return nil
	})
	for fn := range missing {
		f.rpcCh <- SetFileRequest{
			Path: fn,
			Info: nil,
		}
	}
}

func (f *FScanner) notificationHandler() {
	active := map[string]int{}
	everBlocking := make(chan string)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		target := everBlocking
		msg := ""
		lowest := 0
		for fn, n := range active {
			if n < lowest {
				lowest = n
				msg = fn
				target = f.fileChanged
			}
		}
		select {
		case <-ticker.C:
			for fn := range active {
				active[fn]--
			}
		case event, ok := <-f.notifications:
			if !ok {
				return
			}
			rel, err := filepath.Rel(f.share, event.Name)
			if err != nil {
				panic(err)
			}
			if filepath.Base(rel)[0] == '.' {
				continue
			}
			active[rel] = 3
		case target <- msg:
			delete(active, msg)
		}
	}
}

func (f *FScanner) readHashCache() (map[string]FileInfo, error) {
	fh, err := os.Open(filepath.Join(getPath(*varStorage), "hashcache.dat"))
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	dec := gob.NewDecoder(gz)
	var c map[string]FileInfo
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	return c, nil
}
func (f *FScanner) hashCacheFlusher(done <-chan void) {
	for range time.Tick(time.Minute) {
		select {
		case <-done:
			return
		default:
		}
		if !f.updateHashCache {
			continue
		}
		fileCacheMtx.Lock()
		if err := f.writeHashCache(fileCache); err != nil {
			log.Printf("Couldn't write hashcache.dat: %v", err)
		}
		f.updateHashCache = false
		fileCacheMtx.Unlock()
	}
}

func (f *FScanner) writeHashCache(c map[string]FileInfo) error {
	fh, err := os.Create(filepath.Join(getPath(*varStorage), "hashcache.dat.new"))
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(fh)
	enc := gob.NewEncoder(gz)
	err = enc.Encode(&c)
	if err != nil {
		gz.Close()
		fh.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		fh.Close()
		return err
	}
	if err := fh.Close(); err != nil {
		return err
	}
	return os.Rename(filepath.Join(getPath(*varStorage), "hashcache.dat.new"), filepath.Join(getPath(*varStorage), "hashcache.dat"))
}
