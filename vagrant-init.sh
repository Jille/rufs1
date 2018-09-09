#!/usr/bin/env bash

set -x

RUFSDIR=/home/vagrant/go/src/rufs/
ipMASTER="198.51.100.42"
portMASTER="1666"

cd ${RUFSDIR}
export GOPATH=/home/vagrant/go/
export PATH=${GOPATH}bin:${PATH}

case $1 in
master)
    echo "We're the master, going to configure rufs-master-bolt"
    make rufs_master
    useradd rufs-master
    mkdir /var/lib/rufs/
    mv ./rufs-master-bolt /usr/local/bin/
    rufs-master-bolt --var_storage /var/lib/rufs/ --master_gen_keys
    cp systemd/rufs-master.service /etc/systemd/system/
    # Store the CA.crt in the synced folder such that the client can access it
    cp /var/lib/rufs/master/ca.crt /public/
    # Export the token for the client
    rufs-master-bolt --var_storage /var/lib/rufs/ --get_auth_token rufsclient > /public/rufsclient.token
    chown -R rufs-master:nogroup /var/lib/rufs
    systemctl enable rufs-master
    systemctl start rufs-master
    exit 0
;;
client)
    echo "We're the client, going to configure common rufs"
    TOKEN=$(cat /public/rufsclient.token)
    make rufs
    mkdir -p /srv/rufs/{others,share}
    cp /public/ca.crt /srv/rufs/rufs-master-ca.crt
    mv ./rufs /srv/rufs/
    useradd rufs
    /srv/rufs/rufs --master="${ipMASTER}:${portMASTER}" --master_cert=/srv/rufs/rufs-master-ca.crt --register_token="${TOKEN}" --user=rufsclient
    sed -i "s/master.rufs.tld/${ipMASTER}/" systemd/rufs-client.service
    sed -i "s/myuser/rufsclient/" systemd/rufs-client.service
    sed -i "s/myuser/vagrant/" systemd/rufs-client.service
    cp systemd/rufs-client.service /etc/systemd/system/
    chown -R rufs:rufs /srv/rufs/
    systemctl enable rufs-client
    systemctl start rufs-client
    exit 0
;;
*)
    echo "Cannot use $1 as option for init.sh"
    exit 1
esac
