#!/bin/bash

apt-get update
apt-get install --yes zfsutils-linux iptables udev net-tools iproute2
apt-get clean

# create /etc/mtab to keep ZFS happy
ln -nfs /proc/mounts /etc/mtab
