// +build !noftp

package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ftpserver "github.com/goftp/server"
	"golang.org/x/net/context"
)

var (
	ftpPort = flag.Int("ftp_port", 0, "The port to run an FTP server at")
)

type FTPd struct {
	FrontendLib
	ftp *ftpserver.Server
}

func init() {
	registerServerModule(func(s *Server) (module, error) {
		if *ftpPort > 0 {
			return newFTPd(*ftpPort, s)
		}
		return nil, nil
	})
}

type FTPdAuth string

func (a FTPdAuth) CheckPasswd(username string, pass string) (bool, error) {
	return string(a) == username, nil
}

func newFTPd(port int, server *Server) (*FTPd, error) {
	f, err := GetFetcher(server)
	if err != nil {
		return nil, err
	}
	ftpd := &FTPd{
		FrontendLib: FrontendLib{
			server:      server,
			cache:       map[string]*GetDirReply{},
			cacheExpiry: map[string]int{},
			fetcher:     f,
		},
	}
	ftpd.ftp = ftpserver.NewServer(&ftpserver.ServerOpts{
		Factory:        ftpd,
		Auth:           FTPdAuth(*user),
		Hostname:       "127.0.0.1",
		PublicIp:       "127.0.0.1",
		Port:           port,
		WelcomeMessage: "Thanks for using RUFS",
	})
	return ftpd, nil
}

func (f *FTPd) Setup() (retErr error) {
	f.master = f.server.master
	return nil
}

func (f *FTPd) Run(ctx context.Context) (retErr error) {
	go func() {
		<-ctx.Done()
		f.ftp.Shutdown()
	}()
	go f.cachePurger(ctx)
	return f.ftp.ListenAndServe()
}

func (f *FTPd) NewDriver() (ftpserver.Driver, error) {
	return &FTPDriver{f}, nil
}

type FTPDriver struct {
	fs *FTPd
}

func (d *FTPDriver) Init(conn *ftpserver.Conn) {
	var err error
	defer LogRPC("ftp.Init", nil, nil, &err)()
}

func (d *FTPDriver) Stat(path string) (fi ftpserver.FileInfo, retErr error) {
	defer LogRPC("ftp.Stat", path, fi, &retErr)()
	path = strings.Trim(filepath.ToSlash(path), "/")
	if path == "" {
		return FTPFileInfo{"/", nil, true}, nil
	}
	dn, fn := filepath.Split(path)
	ret, err := d.fs.GetDirCached(dn)
	if err != nil {
		return nil, err
	}
	if f, found := ret.Files[fn]; found {
		return FTPFileInfo{fn, &f, false}, nil
	}
	for _, d := range ret.Dirs {
		if d == fn {
			return FTPFileInfo{fn, nil, true}, nil
		}
	}
	return nil, errors.New("File not found")
}

func (d *FTPDriver) ChangeDir(dir string) (retErr error) {
	defer LogRPC("ftp.ChangeDir", dir, nil, &retErr)()
	dir = filepath.ToSlash(dir)
	info, err := d.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("not a directory")
	}
	return nil
}

func (d *FTPDriver) ListDir(dir string, cb func(i ftpserver.FileInfo) error) (retErr error) {
	defer LogRPC("ftp.ListDir", dir, nil, &retErr)()
	dir = filepath.ToSlash(dir)
	ret, err := d.fs.GetDirCached(dir)
	if err != nil {
		return err
	}

	dns := sort.StringSlice{}
	for _, dn := range ret.Dirs {
		dns = append(dns, dn)
	}
	dns.Sort()
	for _, dn := range dns {
		if err := cb(FTPFileInfo{dn, nil, true}); err != nil {
			return err
		}
	}
	fns := sort.StringSlice{}
	for fn := range ret.Files {
		fns = append(fns, fn)
	}
	fns.Sort()
	for _, fn := range fns {
		i := ret.Files[fn]
		if err := cb(FTPFileInfo{fn, &i, false}); err != nil {
			return err
		}
	}
	return nil
}

func (d *FTPDriver) DeleteDir(dir string) error {
	return errors.New("RUFS FTP is readonly")
}

func (d *FTPDriver) DeleteFile(fn string) error {
	return errors.New("RUFS FTP is readonly")
}

func (d *FTPDriver) Rename(old, new string) error {
	return errors.New("RUFS FTP is readonly")
}

func (d *FTPDriver) MakeDir(dir string) error {
	return errors.New("RUFS FTP is readonly")
}

func (d *FTPDriver) GetFile(fn string, offset int64) (size int64, stream io.ReadCloser, retErr error) {
	defer LogRPC("ftp.GetFile", fn, nil, &retErr)()
	fn = filepath.ToSlash(fn)
	fi, err := d.fs.GetFileInfo(fn)
	var ret *GetOwnersReply
	if err == nil {
		ret, err = d.fs.master.GetOwners(fi.Hash)
	}
	if err != nil {
		return 0, nil, err
	}
	pfh, err := d.fs.fetcher.NewHandle(fi.Hash, fi.Size, ret.Owners)
	if err != nil {
		return 0, nil, err
	}
	return fi.Size, pfh.Stream(offset), nil
}

func (d *FTPDriver) PutFile(fn string, r io.Reader, append bool) (int64, error) {
	return 0, errors.New("RUFS FTP is readonly")
}

type FTPFileInfo struct {
	name string
	info *FileInfo
	dir  bool
}

func (i FTPFileInfo) Name() string {
	return i.name
}

func (i FTPFileInfo) Size() int64 {
	if i.dir {
		return 0
	}
	return i.info.Size
}

func (i FTPFileInfo) Mode() (ret os.FileMode) {
	ret = 0444
	if i.dir {
		ret |= os.ModeDir
	}
	return ret
}

func (i FTPFileInfo) ModTime() time.Time {
	if i.dir {
		return time.Unix(0, 0)
	}
	return i.info.Mtime
}

func (i FTPFileInfo) IsDir() bool {
	return i.dir
}

func (i FTPFileInfo) Sys() interface{} {
	return nil
}

func (i FTPFileInfo) Owner() string {
	return "rufs"
}

func (i FTPFileInfo) Group() string {
	return "rufs"
}
