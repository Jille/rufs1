package main

import (
	"crypto/tls"
	"fmt"
	"net/rpc"
	"time"
)

type void struct{}

type RUFSMasterService struct {
	master *Master
	conn   *tls.Conn
	peer   *Peer
}

type FileInfo struct {
	Size  int64
	Hash  string
	Mtime time.Time
}

type RUFSMasterClient struct {
	addr   string
	tlsCfg *tls.Config
	*rpc.Client
	*tls.Conn
	reconnCb func(c *RUFSMasterClient) error
}

func NewRUFSMasterClient(addr string, tlsCfg *tls.Config) (*RUFSMasterClient, error) {
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return &RUFSMasterClient{}, err
	}
	client := rpc.NewClient(conn)
	return &RUFSMasterClient{addr, tlsCfg, client, conn, func(c *RUFSMasterClient) error { return nil }}, nil
}

func (c *RUFSMasterClient) Close() error {
	return c.Client.Close()
}

func (c *RUFSMasterClient) SetReconnectCallback(cb func(c *RUFSMasterClient) error) {
	c.reconnCb = cb
}

func (c *RUFSMasterClient) AppendReconnectCallback(cb func(c *RUFSMasterClient) error) {
	old := c.reconnCb
	c.reconnCb = func(c *RUFSMasterClient) error {
		if err := old(c); err != nil {
			return err
		}
		return cb(c)
	}
}

func (c *RUFSMasterClient) Call(method string, q, r interface{}) error {
	err := c.Client.Call(method, q, r)
	if err == rpc.ErrShutdown {
		rc, err := NewRUFSMasterClient(c.addr, c.tlsCfg)
		if err != nil {
			return fmt.Errorf("reconnecting to master failed: %v", err)
		}
		c.Client = rc.Client
		c.Conn = rc.Conn
		cb := c.reconnCb
		c.reconnCb = func(c *RUFSMasterClient) error { return nil }
		defer func() {
			c.reconnCb = cb
		}()
		if err := cb(c); err != nil {
			return fmt.Errorf("reconnecting to master failed: %v", err)
		}
		return c.Client.Call(method, q, r)
	}
	return err
}

// rpc RUFSMasterService.Register(RegisterRequest, RegisterReply) error
type RegisterRequest struct {
	User      string
	Token     string
	PublicKey []byte
}

type RegisterReply struct {
	Certificate []byte
}

func (c *RUFSMasterClient) Register(user, token string, pub []byte) (ret RegisterReply, err error) {
	q := RegisterRequest{
		User:      user,
		Token:     token,
		PublicKey: pub,
	}
	err = c.Call("RUFSMasterService.Register", q, &ret)
	return ret, err
}

// rpc RUFSMasterService.Signin(SigninRequest, SigninReply) error
type SigninRequest struct {
	ProtocolVersion int
	User            string
	Address         string
}

type SigninReply struct {
}

func (c *RUFSMasterClient) Signin(addr, user string) (ret SigninReply, err error) {
	q := SigninRequest{
		ProtocolVersion: 1,
		Address:         addr,
		User:            user,
	}
	err = c.Call("RUFSMasterService.Signin", q, &ret)
	return ret, err
}

// rpc RUFSMasterService.SetFile(SetFileRequest, SetFileReply) error
type SetFileRequest struct {
	Path string
	Info *FileInfo
}

type SetFileReply struct {
}

func (c *RUFSMasterClient) SetFile(q SetFileRequest) error {
	ret := SetFileReply{}
	return c.Call("RUFSMasterService.SetFile", q, &ret)
}

// rpc RUFSMasterService.GetDir(GetDirRequest, GetDirReply) error
type GetDirRequest struct {
	Path string
}

type GetDirReply struct {
	Dirs  []string
	Files map[string]FileInfo
}

func (c *RUFSMasterClient) GetDir(path string) (ret *GetDirReply, err error) {
	q := GetDirRequest{
		Path: path,
	}
	err = c.Call("RUFSMasterService.GetDir", q, &ret)
	return ret, err
}

// rpc RUFSMasterService.GetOwners(GetOwnersRequest, GetOwnersReply) error
type GetOwnersRequest struct {
	Hash string
}

type GetOwnersReply struct {
	Owners []string
}

func (c *RUFSMasterClient) GetOwners(hash string) (ret *GetOwnersReply, err error) {
	q := GetOwnersRequest{
		Hash: hash,
	}
	err = c.Call("RUFSMasterService.GetOwners", q, &ret)
	return ret, err
}

type RUFSService struct{}

type RUFSClient struct {
	addr   string
	tlsCfg *tls.Config
	*rpc.Client
	*tls.Conn
}

func NewRUFSClient(addr string, tlsCfg *tls.Config) (*RUFSClient, error) {
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return &RUFSClient{}, err
	}
	client := rpc.NewClient(conn)
	return &RUFSClient{addr, tlsCfg, client, conn}, nil
}

func (c *RUFSClient) Close() error {
	return c.Client.Close()
}

func (c *RUFSClient) Call(method string, q, r interface{}) error {
	err := c.Client.Call(method, q, r)
	if err == rpc.ErrShutdown {
		rc, err := NewRUFSClient(c.addr, c.tlsCfg)
		if err != nil {
			return err
		}
		c.Client = rc.Client
		c.Conn = rc.Conn
		return c.Client.Call(method, q, r)
	}
	return err
}

// rpc RUFSService.Ping(PingRequest, PingReply) error
type PingRequest struct {
}

type PingReply struct {
}

func (c *RUFSClient) Ping() error {
	q := PingRequest{}
	ret := PingReply{}
	return c.Call("RUFSService.Ping", q, &ret)
}

// rpc RUFSService.Read(ReadRequest, ReadReply) error
type ReadRequest struct {
	Hash   string
	Offset int64
	Size   int
}

type ReadReply struct {
	Data []byte
	EOF  bool
}

func (c *RUFSClient) Read(hash string, offset int64, size int) (ret *ReadReply, err error) {
	q := ReadRequest{
		Hash:   hash,
		Offset: offset,
		Size:   size,
	}
	err = c.Call("RUFSService.Read", q, &ret)
	return ret, err
}
