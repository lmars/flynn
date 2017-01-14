#!/bin/bash

apt-get update
apt-get install --yes \
  zfsutils-linux \
  iptables \
  udev \
  net-tools \
  iproute2 \
  qemu-kvm \
  linux-image-generic-lts-xenial

# install jq for reading /.containerconfig
curl -fsLo "/usr/local/bin/jq" "http://stedolan.github.io/jq/download/linux64/jq"
chmod +x "/usr/local/bin/jq"

# create /etc/mtab to keep ZFS happy
ln -nfs /proc/mounts /etc/mtab

# add 9P modules to initramfs
cat >> /etc/initramfs-tools/modules <<EOF
9p
9pnet
9pnet_virtio
EOF
update-initramfs -u

# start flynn-host on VM startup
cat > /lib/systemd/system/flynn-host.service <<EOF
[Unit]
Description=Flynn host daemon
Documentation=https://flynn.io/docs
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/flynn-host daemon
Restart=on-failure

# set delegate yes so that systemd does not reset the cgroups of containers
Delegate=yes

# kill only the flynn-host process, not all processes in the cgroup
KillMode=process

[Install]
WantedBy=multi-user.target
EOF
systemctl enable flynn-host.service

# remove the initctl diversion and rc.d policy so systemd works in VMs
rm /usr/sbin/policy-rc.d
rm /sbin/initctl
dpkg-divert --rename --remove /sbin/initctl
