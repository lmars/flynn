#!/bin/bash

apt-get update
apt-get install --yes curl iproute2 qemu-kvm
apt-get clean

curl -fsSLo /usr/local/bin/jq https://github.com/stedolan/jq/releases/download/jq-1.5/jq-linux64
chmod +x /usr/local/bin/jq

cp util/kvm/start.sh /bin/start-kvm
