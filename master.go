package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/rpc"
	"os"
	"strings"
)

type Peer struct {
	user    string
	address string
	ident   string
	conn    *tls.Conn
}

var (
	masterDir = flag.String("master_dir", "%rufs_var_storage%/master/", "Where to store data from the master process")
)

type Master struct {
	port  int
	dir   string
	sock  net.Listener
	vault *MasterVault
	db    *Database
}

func newMaster(port int) (*Master, error) {
	dir := getPath(*masterDir)
	ensureDirExists(dir)
	vault, err := loadMasterVault(dir)
	if err != nil {
		return nil, err
	}

	db, err := newDatabase()
	if err != nil {
		return nil, err
	}

	return &Master{
		port:  port,
		dir:   dir,
		vault: vault,
		db:    db,
	}, nil
}

func (m *Master) Setup() error {
	if *getAuthToken != "" {
		token := createAuthToken(m.vault, *getAuthToken)
		log.Printf("Auth token for %s: %s", *getAuthToken, token)
		fmt.Println(token)
		os.Exit(0)
	}

	tlsCfg := getTlsConfig(TlsConfigMaster, m.vault.ca, m.vault.getTlsCert(), "rufs-master")

	sock, err := tls.Listen("tcp", fmt.Sprintf(":%d", m.port), tlsCfg)
	if err != nil {
		return err
	}
	m.sock = sock
	return nil
}

func (m *Master) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		m.sock.Close()
	}()
	for {
		conn, err := m.sock.Accept()
		if err != nil {
			break
		}
		tlsConn := conn.(*tls.Conn)
		peer := &Peer{}
		s := rpc.NewServer()
		s.Register(RUFSMasterService{m, tlsConn, peer})
		go func() {
			if err := tlsConn.Handshake(); err != nil {
				log.Printf("handshake failed: %v", err)
				return
			}
			defer peer.Disconnected(m)
			s.ServeConn(conn)
		}()
	}
	return nil
}

func (s RUFSMasterService) Register(q RegisterRequest, r *RegisterReply) (retErr error) {
	defer LogRPC("Register", q, r, &retErr)()
	token := createAuthToken(s.master.vault, q.User)
	if token != q.Token {
		return errors.New("Token is invalid")
	}

	cert, err := signClient(s.master.vault, q.PublicKey, q.User)
	if err != nil {
		return fmt.Errorf("Failed to sign certificate: %v", err)
	}
	r.Certificate = cert

	return nil
}

func (s RUFSMasterService) Signin(q SigninRequest, r *SigninReply) (retErr error) {
	defer LogRPC("Signin", q, r, &retErr)()
	if q.ProtocolVersion != 1 {
		return fmt.Errorf("Protocol %d not supported", q.ProtocolVersion)
	}
	if q.User == "" {
		return fmt.Errorf("Invalid credentials for user %q", q.User)
	}
	tlsState := s.conn.ConnectionState()
	if len(tlsState.VerifiedChains) == 0 {
		return errors.New("Signin is only possible with a valid certificate")
	}
	leaf := tlsState.VerifiedChains[0][0]
	if leaf.Subject.CommonName != q.User {
		return errors.New("User does not match certificate")
	}
	if addr := q.Address; addr != "" {
		if strings.HasPrefix(addr, ":") {
			host, _, _ := net.SplitHostPort(s.conn.RemoteAddr().String())
			addr = net.JoinHostPort(host, addr[1:])
		}
		tlsCfg := getTlsConfig(TlsConfigServerClient, s.master.vault.ca, s.master.vault.getTlsCert(), q.User)
		client, err := NewRUFSClient(addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("Failed to connect back to %q: %v", addr, err)
		}
		err = client.Ping()
		if err != nil {
			client.Close()
			return fmt.Errorf("Failed to ping to %q: %v", addr, err)
		}
		s.peer.address = addr
		s.peer.ident = fmt.Sprintf("%s@%s", q.User, addr)
	}
	s.peer.user = q.User
	return nil
}

func (s RUFSMasterService) SetFile(q SetFileRequest, r *SetFileReply) (retErr error) {
	defer LogRPC("SetFile", q, r, &retErr)()
	if s.peer.address == "" {
		return errors.New("SetFile denied before calling Signin with Address")
	}
	ex := append([]string{s.peer.user}, strings.Split(q.Path, "/")...)
	for _, p := range ex {
		if len(p) == 0 || p[0] == '.' {
			return fmt.Errorf("Invalid part in filename: %q", p)
		}
	}
	return s.master.db.SetFile(ex, q.Info, s.peer.ident)
}

func (s RUFSMasterService) GetDir(q GetDirRequest, r *GetDirReply) (retErr error) {
	defer LogRPC("GetDir", q, r, &retErr)()
	if s.peer.user == "" {
		return errors.New("GetDir denied before calling Signin")
	}
	path := strings.Trim(q.Path, "/")
	files, dirs, err := s.master.db.GetDir(path)
	if err != nil {
		return err
	}
	r.Dirs = dirs
	r.Files = files
	return nil
}

func (s RUFSMasterService) GetOwners(q GetOwnersRequest, r *GetOwnersReply) (retErr error) {
	defer LogRPC("GetOwners", q, r, &retErr)()
	if s.peer.user == "" {
		return errors.New("GetOwners denied before calling Signin")
	}
	r.Owners, retErr = s.master.db.GetOwners(q.Hash)
	return retErr
}

func (p *Peer) Disconnected(m *Master) {
	var err error
	defer LogRPC("[disconnect]", nil, nil, &err)()
	if p.address == "" {
		return
	}

	m.db.PeerDisconnected(p.ident)
}

func genMasterKeys() error {
	ensureDirExists(getPath(*masterDir))
	log.Println("Generating and signing key pair...")
	if err := createCA(getPath(*masterDir), "rufs-master"); err != nil {
		return err
	}
	log.Println("Keys generated. Testing them")
	if _, err := loadMasterVault(getPath(*masterDir)); err != nil {
		return err
	}
	log.Println("You're good to go!")
	return nil
}
