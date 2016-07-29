// +build !windows,!nofuse

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	osUser "os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"golang.org/x/net/context"
)

var (
	allowUsers = flag.String("allow_users", "", "Which local users to allow access to the fuse mount, comma separated")
	mountpoint = flag.String("mountpoint", "", "Where to mount everyone's stuff")
)

type FuseMnt struct {
	mountpoint   string
	allowedUsers map[uint32]bool
	server       *Server
	master       *RUFSMasterClient
	fetcher      *Fetcher
	cache        map[string]*GetDirReply
	cacheExpiry  map[string]int
	cacheMtx     sync.Mutex
}

func init() {
	registerServerModule(func(s *Server) (module, error) {
		if *mountpoint != "" {
			return newFuseMnt(*mountpoint, s)
		}
		return nil, nil
	})
}

func newFuseMnt(mountpoint string, server *Server) (*FuseMnt, error) {
	var allowedUsers map[uint32]bool
	if *allowUsers != "" {
		allowedUsers = map[uint32]bool{}
		for _, u := range strings.Split(*allowUsers, ",") {
			pwd, err := osUser.Lookup(u)
			if err != nil {
				return nil, err
			}
			s, _ := strconv.ParseUint(pwd.Uid, 10, 32)
			allowedUsers[uint32(s)] = true
		}
	}
	return &FuseMnt{
		server:       server,
		mountpoint:   mountpoint,
		allowedUsers: allowedUsers,
		cache:        map[string]*GetDirReply{},
		cacheExpiry:  map[string]int{},
		fetcher:      NewFetcher(server),
	}, nil
}

func (f *FuseMnt) Setup() (retErr error) {
	f.master = f.server.master
	return nil
}

func (f *FuseMnt) Run(done <-chan void) (retErr error) {
	fuse.Debug = func(msg interface{}) { fmt.Println(msg) }
	options := []fuse.MountOption{
		fuse.FSName("rufs"),
		fuse.Subtype("rufs"),
		fuse.VolumeName("rufs"),
		fuse.ReadOnly(),
		fuse.MaxReadahead(1024 * 1024),
	}
	if len(f.allowedUsers) != 0 {
		options = append(options, fuse.AllowOther())
	}
	conn, err := fuse.Mount(f.mountpoint, options...)
	if err != nil {
		return err
	}
	go func() {
		<-done
		select {
		case <-conn.Ready:
			if conn.MountError != nil {
				return
			}
			if err := fuse.Unmount(f.mountpoint); err != nil {
				log.Printf("Failed to unmount %q: %v", mountpoint, err)
			}
		case <-time.After(5 * time.Second):
			conn.Close()
		}
	}()
	fsDone := make(chan void)
	defer close(fsDone)
	go f.cachePurger(fsDone)
	defer func() {
		if err := conn.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()
	if err := fs.Serve(conn, f); err != nil {
		return err
	}
	<-conn.Ready
	return conn.MountError
}

func (fs *FuseMnt) cachePurger(done <-chan void) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		fs.cacheMtx.Lock()
		for dn, exp := range fs.cacheExpiry {
			if exp <= 1 {
				delete(fs.cacheExpiry, dn)
				delete(fs.cache, dn)
			} else {
				fs.cacheExpiry[dn]--
			}
		}
		fs.cacheMtx.Unlock()
	}
}

func (fs *FuseMnt) purgeCacheEntry(dn string) {
	fs.cacheMtx.Lock()
	defer fs.cacheMtx.Unlock()
	if _, ok := fs.cache[dn]; ok {
		delete(fs.cacheExpiry, dn)
		delete(fs.cache, dn)
	}
}

func (fs *FuseMnt) GetDirCached(dn string) (*GetDirReply, error) {
	fs.cacheMtx.Lock()
	if gdr, ok := fs.cache[dn]; ok {
		fs.cacheMtx.Unlock()
		return gdr, nil
	}
	fs.cacheMtx.Unlock()
	ret, err := fs.master.GetDir(dn)
	if err != nil {
		if err.Error() == "ENOENT" {
			return nil, fuse.ENOENT
		}
		return nil, err
	}
	fs.cacheMtx.Lock()
	fs.cache[dn] = ret
	fs.cacheExpiry[dn] = 3
	fs.cacheMtx.Unlock()
	return ret, nil
}

func (fs *FuseMnt) GetFileInfo(fn string) (fi FileInfo, retErr error) {
	dn, fn := filepath.Split(fn)
	ret, err := fs.GetDirCached(dn)
	if err != nil {
		return fi, err
	}
	if fi, found := ret.Files[fn]; found {
		return fi, nil
	}
	return fi, fuse.ENOENT
}

func (fs *FuseMnt) Root() (fs.Node, error) {
	return &dir{node{fs, ""}}, nil
}

type node struct {
	fs   *FuseMnt
	path string
}

func (n *node) checkAccess(uid uint32) error {
	if n.fs.allowedUsers == nil {
		return nil
	}
	if !n.fs.allowedUsers[uid] {
		return fuse.EPERM
	}
	return nil
}

func (n *node) Access(ctx context.Context, req *fuse.AccessRequest) (retErr error) {
	return n.checkAccess(req.Header.Uid)
}

func (n *node) Attr(ctx context.Context, attr *fuse.Attr) (retErr error) {
	if n.path == "" {
		attr.Mode = 0755 | os.ModeDir
		return nil
	}
	dn, fn := filepath.Split(n.path)
	ret, err := n.fs.GetDirCached(dn)
	if err != nil {
		return err
	}
	if f, found := ret.Files[fn]; found {
		attr.Size = uint64(f.Size)
		attr.Mode = 0644
		attr.Mtime = f.Mtime
		return nil
	}
	for _, d := range ret.Dirs {
		if d == fn {
			attr.Mode = 0755 | os.ModeDir
			return nil
		}
	}
	return fuse.ENOENT
}

func (n *node) Setattr(ctx context.Context, request *fuse.SetattrRequest, response *fuse.SetattrResponse) (retErr error) {
	return fuse.ENOSYS
}

func (n *node) Getxattr(ctx context.Context, req *fuse.GetxattrRequest, resp *fuse.GetxattrResponse) (retErr error) {
	return fuse.ENOSYS
}

func (n *node) Setxattr(ctx context.Context, req *fuse.SetxattrRequest) (retErr error) {
	return fuse.ENOSYS
}

type dir struct {
	node
}

func (d *dir) Create(ctx context.Context, request *fuse.CreateRequest, response *fuse.CreateResponse) (_ fs.Node, _ fs.Handle, retErr error) {
	return nil, nil, fuse.ENOSYS
}

func (d *dir) Lookup(ctx context.Context, name string) (_ fs.Node, retErr error) {
	path := filepath.Join(d.path, name)
	ret, err := d.fs.GetDirCached(d.path)
	if err != nil {
		return nil, err
	}
	if _, found := ret.Files[name]; found {
		return &file{node{d.fs, path}}, nil
	}
	for _, dn := range ret.Dirs {
		if dn == name {
			return &dir{node{d.fs, path}}, nil
		}
	}
	d.fs.purgeCacheEntry(d.path)
	return nil, fuse.ENOENT
}

func (d *dir) Mkdir(ctx context.Context, request *fuse.MkdirRequest) (_ fs.Node, retErr error) {
	return nil, fuse.ENOSYS
}

func (d *dir) ReadDirAll(ctx context.Context) (_ []fuse.Dirent, retErr error) {
	ret, err := d.fs.GetDirCached(d.path)
	if err != nil {
		return nil, err
	}

	dirents := make([]fuse.Dirent, 0, len(ret.Dirs)+len(ret.Files))
	for _, d := range ret.Dirs {
		dirents = append(dirents, fuse.Dirent{
			Name: d,
			Type: fuse.DT_Dir,
		})
	}
	for fn := range ret.Files {
		dirents = append(dirents, fuse.Dirent{
			Name: fn,
			Type: fuse.DT_File,
		})
	}
	return dirents, nil
}

func (d *dir) Remove(ctx context.Context, request *fuse.RemoveRequest) error {
	return fuse.ENOSYS
}

type file struct {
	node
}

func (f *file) Open(ctx context.Context, request *fuse.OpenRequest, response *fuse.OpenResponse) (_ fs.Handle, retErr error) {
	if err := f.checkAccess(request.Header.Uid); err != nil {
		return nil, err
	}
	fi, err := f.fs.GetFileInfo(f.path)
	var ret *GetOwnersReply
	if err == nil {
		ret, err = f.fs.master.GetOwners(fi.Hash)
	}
	if err != nil {
		f.fs.purgeCacheEntry(f.path)
		if err.Error() == "ENOENT" {
			return nil, fuse.ENOENT
		}
		return nil, err
	}
	pfh, err := f.fs.fetcher.NewHandle(fi.Hash, fi.Size, ret.Owners)
	if err != nil {
		return nil, err
	}
	return &handle{f.node, pfh}, nil
}

type handle struct {
	node
	pfh *pfHandle
}

func (h *handle) Read(ctx context.Context, request *fuse.ReadRequest, response *fuse.ReadResponse) (retErr error) {
	response.Data, retErr = h.pfh.Read(ctx, request.Offset, request.Size)
	return retErr
}

func (h *handle) Write(ctx context.Context, request *fuse.WriteRequest, response *fuse.WriteResponse) (retErr error) {
	return fuse.ENOSYS
}

func (h *handle) Fsync(ctx context.Context, request *fuse.FsyncRequest) error {
	return fuse.ENOSYS
}

func (h *handle) Release(ctx context.Context, request *fuse.ReleaseRequest) error {
	h.pfh.Close()
	return nil
}
