package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Jille/rufs/common"
	"github.com/Jille/rufs/filescanner"
	"golang.org/x/net/context"
)

var (
	port          = flag.Int("port", 1667, "Flag to run the server at")
	extIp         = flag.String("external_ip", "", "Your external IP (if not detected automatically)")
	autoIp        = flag.Bool("auto_ip", false, "Automatically determine public/external IP")
	share         = flag.String("share", "", "Share this folder")
	user          = flag.String("user", "quis", "Who are you?")
	registerToken = flag.String("register_token", "", "Register with the master and get certificates")
	masterCert    = flag.String("master_cert", "%rufs_var_storage%/master/ca.crt", "Path to ca file of the master")
)

type Server struct {
	masterAddr         string
	master             *RUFSMasterClient
	sock               net.Listener
	shares             map[string]string
	ca                 *x509.Certificate
	cert               *tls.Certificate
	hashToPathMtx      sync.Mutex
	hashToPath         map[string]map[string]void
	setFileRequestChan chan SetFileRequest
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
	shares := map[string]string{}
	if *share != "" {
		for _, s := range strings.Split(*share, ",") {
			sp := strings.SplitN(s, "=", 2)
			if len(sp) == 2 {
				shares[sp[0]] = getPath(sp[1])
			} else {
				shares[""] = getPath(s)
			}
		}
		if len(shares) != strings.Count(*share, ",")+1 {
			return nil, errors.New("Flag --share seems has duplicate aliases")
		}
	}
	return &Server{
		masterAddr: master,
		shares:     shares,
		ca:         ca,
		cert:       cert,
		hashToPath: map[string]map[string]void{},
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
	srv.Register(RUFSService{s})
	go srv.Accept(s.sock)

	var addr string
	if *share != "" || *extIp != "" || *autoIp {
		if *autoIp {
			ip, err := determineIP()
			if err != nil {
				log.Printf("Automatic IP detection failed: %v", err)
			} else {
				log.Printf("Automatically determined external IP to be %s", ip)
				*extIp = ip
			}
		}
		addr = fmt.Sprintf("%s:%d", *extIp, *port)
	}
	signin := func(c *RUFSMasterClient) error {
		_, err = c.Signin(addr, *user)
		if err != nil {
			return fmt.Errorf("Signin failed: %v", err)
		}
		return nil
	}
	if err := signin(s.master); err != nil {
		return err
	}
	s.master.SetReconnectCallback(signin)

	return nil
}

func (s *Server) Run(ctx context.Context) error {
	defer s.master.Close()
	defer s.sock.Close()

	if len(s.shares) > 0 {
		s.setFileRequestChan = s.startSetFileThreads()
		for share, baseDir := range s.shares {
			share := share
			setFile := func(ctx context.Context, path string, info *common.FileInfo, old *common.FileInfo) {
				s.SetFile(ctx, share, path, info, old)
			}
			mc := filescanner.New(baseDir, getPath(*varStorage))
			ectx, cancel := context.WithCancel(ctx)
			mc.ContinuousExport(ectx, setFile)
			s.master.AppendReconnectCallback(func(c *RUFSMasterClient) error {
				cancel()
				ectx, cancel = context.WithCancel(ctx)
				mc.ContinuousExport(ectx, setFile)
				return nil
			})
			go mc.Run(ctx)
		}
	}
	<-ctx.Done()
	return nil
}

func (s *Server) startSetFileThreads() chan SetFileRequest {
	rpc := make(chan SetFileRequest)
	for i := 0; 4 > i; i++ {
		go func() {
			for req := range rpc {
				if err := s.master.SetFile(req); err != nil {
					log.Printf("SetFile(%+v) failed: %v", req, err)
				}
			}
		}()
	}
	return rpc
}

func (s *Server) SetFile(ctx context.Context, share, path string, info *common.FileInfo, old *common.FileInfo) {
	sharePlusPath := share + "\x00" + path
	s.hashToPathMtx.Lock()
	if old != nil {
		delete(s.hashToPath[old.Hash], sharePlusPath)
		if len(s.hashToPath[old.Hash]) == 0 {
			delete(s.hashToPath, old.Hash)
		}
	}
	if info != nil {
		if _, ok := s.hashToPath[info.Hash]; !ok {
			s.hashToPath[info.Hash] = map[string]void{}
		}
		s.hashToPath[info.Hash][sharePlusPath] = void{}
	}
	s.hashToPathMtx.Unlock()

	s.setFileRequestChan <- SetFileRequest{
		Path: filepath.Join(share, path),
		Info: info,
	}
}

func (RUFSService) Ping(q PingRequest, r *PingReply) (retErr error) {
	defer LogRPC("Ping", q, r, &retErr)()
	return nil
}

func (s RUFSService) Read(q ReadRequest, r *ReadReply) (retErr error) {
	var rc ReadReply
	l := LogRPC("Read", q, &rc, &retErr)
	defer func() {
		rc = *r
		rc.Data = nil
		l()
	}()
	s.server.hashToPathMtx.Lock()
	paths, ok := s.server.hashToPath[q.Hash]
	s.server.hashToPathMtx.Unlock()
	if !ok || len(paths) == 0 {
		return errors.New("ENOENT")
	}
	var file *os.File
	var err error
	for path := range paths {
		sp := strings.Split(path, "\x00")
		file, err = os.Open(filepath.Join(s.server.shares[sp[0]], sp[1]))
		if err == nil {
			defer file.Close()
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

func determineIP() (string, error) {
	res, err := http.Get("https://api.ipify.org")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	ip, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return string(ip), nil
}
