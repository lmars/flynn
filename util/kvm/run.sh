#!/bin/bash

set -e

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "${ROOT}/script/lib/ui.sh"
source "${ROOT}/script/lib/util.sh"

usage() {
  cat <<USAGE >&2
usage: $0 [options] DISKS...

Run a VM image in Flynn.

OPTIONS:
  -h, --help       Show this message
  -k, --kvm-args   KVM args
USAGE
}

main() {
  local kvm_args="-m 1024 -smp 2"

  while true; do
    case "$1" in
      -h | --help)
        usage
        exit 0
        ;;
      -k | --kvm-args)
        if [[ -z "$2" ]]; then
          fail "--kvm-args flag requires an argument"
        fi
        kvm_args="$2"
        shift 2
        ;;
      *)
        break
        ;;
    esac
  done

  if [[ $# -eq 0 ]]; then
    usage
    exit 1
  fi

  # mounts the disks at /vm/diskN.img
  local disks=()
  local mounts=()
  local index=0
  for disk in $@; do
    if ! [[ -s "${disk}" ]]; then
      fail "no such disk: ${disk}"
    fi
    local path="/vm/disk${index}.img"
    disks+=("${path}")
    mounts+=("$(readlink -f "${disk}"):${path}")
    index=$((index+1))
  done

  local image="$(jq .kvm "${ROOT}/build/manifests/images.json")"
  if [[ -z "${image}" ]]; then
    fail "failed to get KVM image config"
  fi

  exec "${ROOT}/build/bin/flynn-host" run \
    --profiles "kvm" \
    --bind "$(join "," ${mounts[@]})" \
    <(echo "${image}") \
    /bin/start-kvm \
    ${disks[@]} \
    -- \
    ${kvm_args}
}

main "$@"
