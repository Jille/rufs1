package main

import (
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"encoding/gob"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Jille/errchain"
)

var (
	port          = flag.Int("port", 1667, "Flag to run the server at")
	extIp         = flag.String("external_ip", "", "Your external IP (if not detected automatically)")
	share         = flag.String("share", "", "Share this folder")
	user          = flag.String("user", "quis", "Who are you?")
	registerToken = flag.String("register_token", "", "Register with the master and get certificates")
	masterCert    = flag.String("master_cert", "%rufs_var_storage%/master/ca.crt", "Path to ca file of the master")

	fileCacheMtx sync.Mutex
	fileCache    = map[string]FileInfo{}
	hashToPath   = map[string]map[string]void{}
)

type Server struct {
	masterAddr string
	master     *RUFSMasterClient
	sock       net.Listener
	share      string
	ca         *x509.Certificate
	cert       *tls.Certificate
}

func newServer(master string) (*Server, error) {
	ca, err := loadCertificate(getPath(*masterCert))
	if err != nil {
		return nil, err
	}
	isFile := func(fn string) bool {
		_, err := os.Stat(fn)
		return err == nil
	}
	certFile := filepath.Join(getPath(*varStorage), fmt.Sprintf("%s.crt", *user))
	keyFile := filepath.Join(getPath(*varStorage), fmt.Sprintf("%s.key", *user))
	var cert *tls.Certificate
	if isFile(certFile) && isFile(keyFile) {
		crt, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		cert = &crt
	}
	return &Server{
		masterAddr: master,
		share:      getPath(*share),
		ca:         ca,
		cert:       cert,
	}, nil
}

func (s *Server) Setup() error {
	if s.cert != nil {
		tlsCfg := getTlsConfig(TlsConfigServer, s.ca, s.cert, *user)

		sock, err := tls.Listen("tcp", fmt.Sprintf(":%d", *port), tlsCfg)
		if err != nil {
			return err
		}
		s.sock = sock
	}

	log.Println("Connecting...")
	tlsCfg := getTlsConfig(TlsConfigMasterClient, s.ca, s.cert, "rufs-master")
	client, err := NewRUFSMasterClient(s.masterAddr, tlsCfg)
	if err != nil {
		return err
	}
	fmt.Println("Connected")
	s.master = client
	if *registerToken != "" {
		return s.getCertificates()
	}
	if s.cert == nil {
		return errors.New("client certificate not found. Maybe you're looking for --register_token?")
	}

	srv := rpc.NewServer()
	srv.Register(RUFSService{})
	go srv.Accept(s.sock)

	var addr string
	if *share != "" || *extIp != "" {
		addr = fmt.Sprintf("%s:%d", *extIp, *port)
	}
	signin := func(c *RUFSMasterClient) error {
		_, err = c.Signin(addr, *user)
		if err != nil {
			return fmt.Errorf("Signin failed: %v", err)
		}
		return nil
	}
	signin(s.master)
	s.master.SetReconnectCallback(signin)

	return nil
}

func (s *Server) Run(done <-chan void) error {
	defer s.master.Close()
	defer s.sock.Close()

	go s.fsScanner(done)
	<-done
	return nil
}

func (s *Server) readHashCache() (c map[string]FileInfo, retErr error) {
	fh, err := os.Open(filepath.Join(getPath(*varStorage), "hashcache.dat"))
	if err != nil {
		return nil, err
	}
	defer errchain.Call(&retErr, fh.Close)
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return nil, err
	}
	defer errchain.Call(&retErr, gz.Close)
	dec := gob.NewDecoder(gz)
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Server) writeHashCache(c map[string]FileInfo) error {
	fh, err := os.Create(filepath.Join(getPath(*varStorage), "hashcache.dat.new"))
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(fh)
	enc := gob.NewEncoder(gz)
	err = enc.Encode(&c)
	errchain.Append(&err, gz.Close())
	errchain.Append(&err, fh.Close())
	if err != nil {
		return err
	}
	return os.Rename(filepath.Join(getPath(*varStorage), "hashcache.dat.new"), filepath.Join(getPath(*varStorage), "hashcache.dat"))
}

func (s *Server) fsScanner(done <-chan void) {
	if len(*share) == 0 {
		return
	}
	walkMtx := sync.Mutex{}
	rpc := make(chan SetFileRequest)
	defer func() {
		walkMtx.Lock()
		close(rpc)
		walkMtx.Unlock()
	}()
	for i := 0; 4 > i; i++ {
		go func() {
			for req := range rpc {
				if err := s.master.SetFile(req); err != nil {
					log.Printf("SetFile(%+v) failed: %v", req, err)
				} else {
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
					fileCacheMtx.Unlock()
				}
			}
		}()
	}

	s.master.AppendReconnectCallback(func(c *RUFSMasterClient) error {
		go func() {
			walkMtx.Lock()
			defer walkMtx.Unlock()
			select {
			case <-done:
				return
			default:
			}
			fileCacheMtx.Lock()
			fcCopy := fileCache
			fileCacheMtx.Unlock()
			for fn, info := range fcCopy {
				rpc <- SetFileRequest{
					Path: fn,
					Info: &info,
				}
			}
		}()
		return nil
	})

	cacheSeed, err := s.readHashCache()
	if err != nil {
		log.Printf("Couldn't load hashcache.dat: %v", err)
	}
	fastPass := (cacheSeed != nil)
	firstPassDone := false
	go func() {
		for range time.Tick(time.Minute) {
			if firstPassDone {
				return
			}
			if fastPass {
				continue
			}
			fileCacheMtx.Lock()
			if err := s.writeHashCache(fileCache); err != nil {
				log.Printf("Couldn't write hashcache.dat: %v", err)
			}
			fileCacheMtx.Unlock()
		}
	}()
	for {
		walkMtx.Lock()
		missing := map[string]bool{}
		for fn := range fileCache {
			missing[fn] = true
		}
		filepath.Walk(*share, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			fmt.Printf("%s (%v, %v)\n", path, info, err)
			rel, err := filepath.Rel(*share, path)
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
			rpc <- SetFileRequest{
				Path: rel,
				Info: &FileInfo{
					Hash:  hash,
					Size:  info.Size(),
					Mtime: info.ModTime(),
				},
			}
			return nil
		})
		cacheSeed = nil
		for fn := range missing {
			rpc <- SetFileRequest{
				Path: fn,
				Info: nil,
			}
		}
		walkMtx.Unlock()
		if fastPass {
			fastPass = false
			continue
		}
		fileCacheMtx.Lock()
		if err := s.writeHashCache(fileCache); err != nil {
			log.Printf("Couldn't write hashcache.dat: %v", err)
		}
		fileCacheMtx.Unlock()
		firstPassDone = true
		select {
		case <-done:
			return
		case <-time.After(time.Minute):
		}
	}
}

func (RUFSService) Ping(q PingRequest, r *PingReply) (retErr error) {
	defer LogRPC("Ping", q, r, &retErr)()
	return nil
}

func (RUFSService) Read(q ReadRequest, r *ReadReply) (retErr error) {
	var rc ReadReply
	l := LogRPC("Read", q, &rc, &retErr)
	defer func() {
		rc = *r
		rc.Data = nil
		l()
	}()
	fileCacheMtx.Lock()
	paths, ok := hashToPath[q.Hash]
	fileCacheMtx.Unlock()
	if !ok {
		return errors.New("ENOENT")
	}
	var file *os.File
	var err error
	for path := range paths {
		file, err = os.Open(filepath.Join(*share, path))
		if err == nil {
			break
		}
	}
	if err != nil {
		return err
	}
	if q.Offset != 0 {
		if _, err := file.Seek(q.Offset, 0); err != nil {
			return err
		}
	}
	buffer := make([]byte, q.Size)
	n, err := file.Read(buffer)
	r.Data = buffer[:n]
	if err == io.EOF {
		return nil
	}
	return err
}

func (s *Server) getCertificates() error {
	dir := getPath(*varStorage)
	ensureDirExists(dir)
	log.Println("Generating key pair...")
	priv, err := createKeyPair(filepath.Join(dir, fmt.Sprintf("%s.key", *user)))
	if err != nil {
		return err
	}
	log.Println("Requesting master to create certificate...")
	pub, err := serializePubKey(priv)
	if err != nil {
		return err
	}
	ret, err := s.master.Register(*user, *registerToken, pub)
	if err != nil {
		return err
	}
	if err := ioutil.WriteFile(filepath.Join(dir, fmt.Sprintf("%s.crt", *user)), ret.Certificate, 0644); err != nil {
		return err
	}
	log.Println("You're good to go!")
	os.Exit(0)
	return nil
}
