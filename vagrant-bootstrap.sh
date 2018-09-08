#!/usr/bin/env bash

echo "deb http://mirror.1000mbps.com/debian/ stretch-backports main" >> /etc/apt/sources.list

apt-get update
apt-get install -y golang-1.10 git
update-alternatives --install /usr/bin/go go /usr/lib/go-1.10/bin/go 100
update-alternatives --install /usr/bin/gofmt gofmt /usr/lib/go-1.10/bin/gofmt 100
mkdir /home/vagrant/go/
echo "export GOPATH=/home/vagrant/go/" >> /home/vagrant/.bashrc
echo 'export PATH=/home/vagrant/go/bin/:${PATH}' >> /home/vagrant/.bashrc
export GOPATH=/home/vagrant/go
go get github.com/golang/dep/cmd/dep
cd /home/vagrant/go/src/
git clone https://github.com/mrngm/rufs
