package filescanner

import (
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Jille/rufs/common"
	"golang.org/x/net/context"
)

type ExportCallback func(ctx context.Context, path string, info *common.FileInfo, oldInfo *common.FileInfo)

type void = struct{}

type listener struct {
	callback ExportCallback
	ctx      context.Context
}

type Scanner struct {
	ctx          context.Context
	baseDir      string
	cacheDir     string
	fileCacheMtx sync.Mutex
	fileCache    map[string]common.FileInfo
	listeners    []listener
}

func New(baseDir, cacheDir string) *Scanner {
	return &Scanner{
		baseDir:   baseDir,
		cacheDir:  cacheDir,
		fileCache: map[string]common.FileInfo{},
	}
}

func (m *Scanner) cacheFileName() string {
	return filepath.Join(m.cacheDir, fmt.Sprintf("hashcache-%s.dat", strings.Replace(m.baseDir, "/", "-", -1)))
}

func (m *Scanner) readHashCache() (map[string]common.FileInfo, error) {
	fh, err := os.Open(m.cacheFileName())
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
	var c map[string]common.FileInfo
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	return c, nil
}

func (m *Scanner) writeHashCache(c map[string]common.FileInfo) error {
	fh, err := os.Create(m.cacheFileName()+".new")
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(fh)
	enc := gob.NewEncoder(gz)
	err = enc.Encode(&c)
	gz.Close()
	fh.Close()
	if err != nil {
		return err
	}
	return os.Rename(m.cacheFileName()+".new", m.cacheFileName())
}

func (m *Scanner) set(path string, fi *common.FileInfo) {
	m.fileCacheMtx.Lock()
	old := m.fileCache[path]
	if fi != nil {
		m.fileCache[path] = *fi
	} else {
		delete(m.fileCache, path)
	}
	m.fileCacheMtx.Unlock()
	for i := 0; len(m.listeners) > i; i++ {
		l := m.listeners[i]
		select {
		case <-l.ctx.Done():
			m.listeners[i] = m.listeners[len(m.listeners)-1]
			m.listeners = m.listeners[:len(m.listeners)-1]
			i--
		default:
			l.callback(l.ctx, path, fi, &old)
		}
	}
}

func (m *Scanner) Run(ctx context.Context) {
	m.fastPass()
	m.normalPassWithIntermediateAutoSaving(ctx)
	for {
		m.save()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
		m.normalPass()
	}
}

func (m *Scanner) fastPass() {
	cacheSeed, err := m.readHashCache()
	if err != nil {
		log.Printf("Couldn't load hashcache.dat: %v", err)
		return
	}

	if err := scanFilesystem(m.baseDir, func(path string, info os.FileInfo) {
		if f, ok := cacheSeed[path]; ok && f.Size == info.Size() && f.Mtime == info.ModTime() {
			m.set(path, &f)
		}
	}); err != nil {
		log.Printf("Failed to do filesystem scan over %q: %v", m.baseDir, err)
	}
}

func (m *Scanner) save() {
	m.fileCacheMtx.Lock()
	defer m.fileCacheMtx.Unlock()
	if err := m.writeHashCache(m.fileCache); err != nil {
		log.Printf("Couldn't write hashcache.dat: %v", err)
	}
}

func (m *Scanner) normalPassWithIntermediateAutoSaving(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	autoSaverDied := make(chan void)
	go func() {
		defer close(autoSaverDied)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
				m.save()
			}
		}
	}()
	m.normalPass()
	cancel()
	<-autoSaverDied
}

func (m *Scanner) normalPass() {
	missing, err := scanFilesystemAndReportMissing(m.baseDir, m.getFileNames(), func(path string, info os.FileInfo) {
		m.fileCacheMtx.Lock()
		if f, ok := m.fileCache[path]; ok && f.Size == info.Size() && f.Mtime == info.ModTime() {
			m.fileCacheMtx.Unlock()
			return
		}
		m.fileCacheMtx.Unlock()
		fullPath := filepath.Join(m.baseDir, path)
		hash, err := common.HashFile(fullPath)
		if err != nil {
			log.Printf("Failed to hash %q: %v", fullPath, err)
			return
		}
		m.set(path, &common.FileInfo{
			Hash:  hash,
			Size:  info.Size(),
			Mtime: info.ModTime(),
		})
	})
	if err != nil {
		log.Printf("Failed to do filesystem scan over %q: %v", m.baseDir, err)
		return
	}
	for fn := range missing {
		m.set(fn, nil)
	}
}

// ContinuousExport will do an initial export, calling your callback repeatedly, then return but keep calling your callback for every modification detected.
func (m *Scanner) ContinuousExport(ctx context.Context, callback ExportCallback) {
	m.fileCacheMtx.Lock()
	cpy := make(map[string]common.FileInfo, len(m.fileCache))
	for fn, fi := range m.fileCache {
		cpy[fn] = fi
	}
	m.listeners = append(m.listeners, listener{callback, ctx})
	m.fileCacheMtx.Unlock()
	for fn, fi := range cpy {
		select {
		case <-ctx.Done():
			return
		default:
		}
		callback(ctx, fn, &fi, nil)
	}
}

func (m *Scanner) getFileNames() map[string]void {
	m.fileCacheMtx.Lock()
	defer m.fileCacheMtx.Unlock()
	names := make(map[string]void, len(m.fileCache))
	for fn := range m.fileCache {
		names[fn] = void{}
	}
	return names
}

func scanFilesystemAndReportMissing(baseDir string, missing map[string]void, callback func(path string, info os.FileInfo)) (map[string]void, error) {
	err := scanFilesystem(baseDir, func(path string, info os.FileInfo) {
		delete(missing, path)
		callback(path, info)
	})
	return missing, err
}

func scanFilesystem(baseDir string, callback func(path string, info os.FileInfo)) error {
	return filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		fmt.Printf("%s (%v, %v)\n", path, info, err)
		rel, err := filepath.Rel(baseDir, path)
		if err != nil {
			panic(err)
		}
		if base := filepath.Base(rel); base[0] == '.' && base != "." && base != ".." {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode()&os.ModeType > 0 {
			return nil
		}
		callback(rel, info)
		return nil
	})
}
