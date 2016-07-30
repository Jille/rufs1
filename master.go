package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/rpc"
	"os"
	"strings"
	"sync"
)

type Peer struct {
	user    string
	address string
	conn    *tls.Conn
}

var (
	masterDir = flag.String("master_dir", "%rufs_var_storage%/master/", "Where to store data from the master process")
)

type Master struct {
	port     int
	dir      string
	sock     net.Listener
	mtx      sync.RWMutex
	fileTree *Directory
	owners   map[string]map[*Peer]int
	vault    *MasterVault
}

func newMaster(port int) (*Master, error) {
	dir := getPath(*masterDir)
	ensureDirExists(dir)
	vault, err := loadMasterVault(dir)
	if err != nil {
		return nil, err
	}

	return &Master{
		port:     port,
		dir:      dir,
		fileTree: &Directory{map[string]*Directory{}, map[string]FileInfo{}, 0},
		owners:   map[string]map[*Peer]int{},
		vault:    vault,
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

func (m *Master) Run(done <-chan void) error {
	go func() {
		<-done
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
	deletion := (q.Info == nil)
	s.master.mtx.Lock()
	defer s.master.mtx.Unlock()
	t := s.master.fileTree
	trace := make([]*Directory, 0, len(ex))
	for i := 0; len(ex)-1 > i; i++ {
		p := ex[i]
		if sd, ok := t.dirs[p]; ok {
			t = sd
		} else {
			if deletion {
				// File didn't even exist.
				return nil
			}
			t.refs++
			t.dirs[p] = &Directory{map[string]*Directory{}, map[string]FileInfo{}, 0}
			t = t.dirs[p]
		}
		trace = append(trace, t)
	}
	fn := ex[len(ex)-1]
	if f, found := t.files[fn]; found {
		s.master.owners[f.Hash][s.peer]--
		if s.master.owners[f.Hash][s.peer] == 0 {
			delete(s.master.owners[f.Hash], s.peer)
		}
	}
	if deletion {
		if _, found := t.files[fn]; found {
			delete(t.files, fn)
			for i := len(trace) - 1; 0 >= i; i++ {
				tr := trace[i]
				tr.refs--
				if tr.refs > 0 || i == 1 {
					break
				}
				parent := trace[i-1]
				delete(parent.dirs, ex[i])
			}
		}
		return nil
	}
	if _, found := t.files[fn]; !found {
		t.refs++
	}
	t.files[fn] = *q.Info
	if _, existent := s.master.owners[q.Info.Hash]; !existent {
		s.master.owners[q.Info.Hash] = map[*Peer]int{}
	}
	s.master.owners[q.Info.Hash][s.peer]++
	return nil
}

func (s RUFSMasterService) GetDir(q GetDirRequest, r *GetDirReply) (retErr error) {
	defer LogRPC("GetDir", q, r, &retErr)()
	if s.peer.user == "" {
		return errors.New("GetDir denied before calling Signin")
	}
	s.master.mtx.RLock()
	defer s.master.mtx.RUnlock()
	path := strings.Trim(q.Path, "/")
	ex := strings.Split(path, "/")
	if path == "" {
		ex = nil
	}
	t := s.master.fileTree
	for _, p := range ex {
		if len(p) == 0 || p[0] == '.' {
			return fmt.Errorf("Invalid part in filename: %q", p)
		}
		if sd, ok := t.dirs[p]; ok {
			t = sd
		} else {
			return fmt.Errorf("ENOENT")
		}
	}
	r.Dirs = make([]string, 0, len(t.dirs))
	for d := range t.dirs {
		r.Dirs = append(r.Dirs, d)
	}
	r.Files = t.files
	return nil
}

func (s RUFSMasterService) GetOwners(q GetOwnersRequest, r *GetOwnersReply) (retErr error) {
	defer LogRPC("GetOwners", q, r, &retErr)()
	if s.peer.user == "" {
		return errors.New("GetOwners denied before calling Signin")
	}
	s.master.mtx.RLock()
	defer s.master.mtx.RUnlock()
	for p := range s.master.owners[q.Hash] {
		r.Owners = append(r.Owners, fmt.Sprintf("%s@%s", p.user, p.address))
	}
	return nil
}

func (p *Peer) Disconnected(m *Master) {
	var err error
	defer LogRPC("[disconnect]", nil, nil, &err)()
	if p.address == "" {
		return
	}

	m.mtx.Lock()
	delete(m.fileTree.dirs, p.user)
	for hash := range m.owners {
		if _, found := m.owners[hash][p]; found {
			delete(m.owners[hash], p)
		}
	}
	m.mtx.Unlock()
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
