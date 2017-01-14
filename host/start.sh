#!/bin/bash
#
# A script to start flynn-host inside a container.

# exit on error
set -e

main() {
  if [[ -e "/dev/kvm" ]]; then
    start_in_kvm
  else
    start_in_container
  fi
}

start_in_container() {
  # start udevd so that ZFS device nodes and symlinks are created in our mount
  # namespace
  /lib/systemd/systemd-udevd --daemon

  # use a unique directory in /var/lib/flynn (which is bind mounted from the
  # host)
  DIR="/var/lib/flynn/${FLYNN_JOB_ID}"
  mkdir -p "${DIR}"

  # create a tmpdir in /var/lib/flynn to avoid ENOSPC when downloading image
  # layers
  export TMPDIR="${DIR}/tmp"
  mkdir -p "${TMPDIR}"

  # use a unique zpool to avoid conflicts with other daemons
  ZPOOL="flynn-${FLYNN_JOB_ID}"

  ARGS=(
    --state      "${DIR}/host-state.bolt"
    --sink-state "${DIR}/sink-state.bolt"
    --volpath    "${DIR}/volumes"
    --log-dir    "${DIR}/logs"
    --zpool-name "${ZPOOL}"
    --no-resurrect
  )

  if [[ -n "${DISCOVERY_SERVICE}" ]]; then
    ARGS+=(
      --discovery-service "${DISCOVERY_SERVICE}"
    )
  fi

  # start flynn-host
  exec /usr/local/bin/flynn-host daemon ${ARGS[@]}
}

start_in_kvm() {
  local ip="$(jq --raw-output '.IP' /.containerconfig)"
  local gateway="$(jq --raw-output '.Gateway' /.containerconfig)"
  local bridge="$(jq --raw-output '.Bridge' /.containerconfig)"
  local hostname="$(jq --raw-output '.Hostname' /.containerconfig)"

  cat > /etc/qemu-ifup <<EOF
#!/bin/bash
ip link set \$1 up
ip link set \$1 master "${bridge}"
EOF

  cat > /etc/systemd/network/default.network <<EOF
[Match]
Name=en*

[Network]
Address=${ip}
Gateway=${gateway}
DNS=${gateway}
EOF
  systemctl enable systemd-networkd

  # apt-get install --yes sudo
  # adduser --disabled-password --gecos "" ubuntu
  # echo %ubuntu ALL=NOPASSWD:ALL > /etc/sudoers.d/ubuntu
  # chmod 0440 /etc/sudoers.d/ubuntu
  # echo ubuntu:ubuntu | chpasswd

  echo "${hostname}" > /etc/hostname

  exec /usr/bin/qemu-system-x86_64 \
    -enable-kvm \
    -m 1024 \
    -smp 2 \
    -kernel "/vmlinuz" \
    -initrd "/initrd.img" \
    -virtfs "fsdriver=local,path=/,security_model=passthrough,mount_tag=rootfs" \
    -append "root=rootfs rootfstype=9p rootflags=trans=virtio,cache=loose rw console=ttyS0" \
    -device "e1000,netdev=net0" \
    -netdev "tap,id=net0" \
    -nographic
}

main $@
