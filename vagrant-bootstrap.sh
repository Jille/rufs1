#!/usr/bin/env bash

set -x

echo "deb http://mirror.1000mbps.com/debian/ stretch-backports main" >> /etc/apt/sources.list

apt-get update
apt-get install -y golang-1.10 git fuse
update-alternatives --install /usr/bin/go go /usr/lib/go-1.10/bin/go 100
update-alternatives --install /usr/bin/gofmt gofmt /usr/lib/go-1.10/bin/gofmt 100
sed -i 's/#user_allow_other/user_allow_other/' /etc/fuse.conf
mkdir /home/vagrant/go/
echo "export GOPATH=/home/vagrant/go/" >> /home/vagrant/.bashrc
echo 'export PATH=/home/vagrant/go/bin/:${PATH}' >> /home/vagrant/.bashrc
export GOPATH=/home/vagrant/go
export PATH=/home/vagrant/go/bin/:${PATH}
go get github.com/golang/dep/cmd/dep
cd /home/vagrant/go/src/
git clone https://github.com/mrngm/rufs
cd rufs
dep ensure -v
