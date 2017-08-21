# RUFS

[![Build Status](https://travis-ci.org/Jille/rufs.png)](https://travis-ci.org/Jille/rufs)

RUFS is a filesystem that makes it easy to conveniently shares files with
others. You need to run one master job which ties everything together and
participants can run servers that connect to the master and publish which files
they have.

RUFS provides a FUSE mount which will show all files shared by all participants.

## Setup for dummies

```
sudo apt-get install golang-1.6
sudo update-alternatives --install /usr/bin/go go /usr/lib/go-1.6/bin/go 100
sudo update-alternatives --install /usr/bin/gofmt gofmt /usr/lib/go-1.6/bin/gofmt 100
cd
mkdir go
export GOPATH=`pwd`/go
mkdir go/src
cd go/src
git clone https://github.com/Jille/rufs.git
cd rufs
go get bazil.org/fuse
go get golang.org/x/net/context
go get github.com/dustin/go-humanize
go get github.com/mattn/go-sqlite3
go get github.com/goftp/server
go get github.com/boltdb/bolt
make
./rufs --master_gen_keys
AUTH_TOKEN=`./rufs --get_auth_token=$USER`
screen -d -m -S rufsmaster ./rufs --master_port=1337
openssl s_client -showcerts -connect localhost:1337 < /dev/null 2>/dev/null | openssl x509 -outform PEM > ~/.rufs/ca.crt
./rufs --master=localhost:1337 --master_cert=$HOME/.rufs/ca.crt --register_token=$AUTH_TOKEN --user=$USER
mkdir ~/rufs-mnt
screen -d -m -S rufs ./rufs --master=localhost:1337 --master_cert=$HOME/.rufs/ca.crt --user=$USER --mountpoint=$HOME/rufs-mnt --share=$HOME/Pictures
```

--master_gen_keys will create the CA crt and private key. This is the very first thing you need.
--gen_auth_token=$USER creates a token based on the private key that allows $USER to self-register
--register_token sends that token to the master, which will sign your pubkey and give you a certificate
you can leave out the openssl commands and just not pass --master_cert, as it'll by default read from the masters directory

## Technical design

There is one master job which exposes a few RPCs:

* Signin - authenticate and give a host:port where to reach the server
* Register - authenticate with key and get a signed cert
* SetFile - publish a file (filename, size, mtime and sha1)
* GetDir - directory listing (returning all info from earlier SetFile calls)
* GetOwners - find out which peers have a certain file (by hash, not filename)

Every participant runs a server job which exposes these RPCs:

* Ping - check whether this node is reachable and working
* Read - read a chunk of data from a file

Every job needs to have a public IP+port which is reachable by all others.

The master never sees any file content, it will see hashes.

When reading a file, the server will connect to all nodes having a file with
that hash and read it from a arbitrary one. (This will be improved later.)

## TODO

* Share multiple paths as multiple shares
* Multi-master support?
* Remove need to manually get server public key
* Don't do chunked Read RPCs. Switch to requesting streams.
  * Store this locally and try to serve that too.
* Switch server.go fileCache / hashToPath / hashcache.dat to Bolt
* Virtual directories (where people can 'symlink' to their own stuff)
