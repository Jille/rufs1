#!/usr/bin/env bash

case $1 in
master)
    echo "We're the master, going to configure rufs-master-bolt"
    exit 0
;;
client)
    echo "We're the client, going to configure common rufs"
    exit 0
;;
*)
    echo "Cannot use $1 as option for init.sh"
    exit 1
esac
