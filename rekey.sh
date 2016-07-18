#!/bin/sh

set -ve

rm -if ~/.rufs/*.crt
rm -if ~/.rufs/*.key
rm -if ~/.rufs/master/*.*

make

./rufs --master_gen_keys
./rufs --master_port=1666 &
P=$!
sleep 1
AUTH_QUIS=`./rufs --get_auth_token=quis`
./rufs --master=localhost:1666 --user=quis --register_token=$AUTH_QUIS
kill $P
wait
