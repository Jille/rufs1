# RUFS

[![Build Status](https://travis-ci.org/mrngm/rufs.svg?branch=master)](https://travis-ci.org/mrngm/rufs)

RUFS is a filesystem that makes it easy to conveniently shares files with
others. You need to run one master job which ties everything together and
participants can run servers that connect to the master and publish which files
they have.

RUFS provides a FUSE mount which will show all files shared by all participants.

## Prerequisites

* golang >= 1.8
* `dep` (`go get github.com/golang/dep/cmd/dep`)

## Setup master

```
make rufs_master
useradd rufs-master
mkdir /var/lib/rufs/
mv ./rufs-master-bolt /usr/local/bin/
chown rufs-master:nogroup /var/lib/rufs
./rufs --var_storage /var/lib/rufs/ --master_gen_keys
# publish the CA certificate stored in /var/lib/rufs/master/ca.crt somewhere for the clients
systemctl enable rufs-master
systemctl start rufs-master
# alternatively: use the command provided in the service file
```

Ensure that external clients can reach port 1666.

To provide tokens, run `rufs-master-bolt --var_storage /var/lib/rufs/ --get-auth-token xyz` and send the token to user `xyz`.

## Setup client

* Ensure that `user_allow_other` is set in `/etc/fuse.conf` and that `/etc/fuse.conf` is readable by the `rufs` process user.
* Edit `/etc/systemd/system/rufs-client.service` to set the correct address for the master connection
* Download the CA-certificate from the master and put it in `/srv/rufs/rufs-master-ca.crt`.
* Acquire a token from the master

```
make rufs
mkdir -p /srv/rufs/{others,share}
mv ./rufs /srv/rufs/
useradd rufs
chown -R rufs:rufs /srv/rufs/
/srv/rufs/rufs --master=rufs.master.tld:1666 --master_cert=/srv/rufs/rufs-master-ca.crt --register_token=TOKEN --user=myuser
systemctl enable rufs-client
systemctl start rufs-client
# alternatively: use the command provided in the service file
```

Ensure that external clients can reach port 1667.

You can now add files to `/srv/rufs/share/` such that they can be indexed and be made available to the clients connected to the master.

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
