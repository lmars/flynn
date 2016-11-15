#!/bin/bash
#
# A script to download and start Docker 1.9.1 using start-stop-daemon

set -eo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DIR="${ROOT}/util/_toolchain"
DOCKER="${DIR}/docker/bin/docker"
LOG="${DIR}/docker/docker.log"
PID="${DIR}/docker/docker.pid"

VERSION="1.9.1"
SHASUM="52286a92999f003e1129422e78be3e1049f963be1888afc3c9a99d5a9af04666"
URL="https://get.docker.com/builds/Linux/x86_64/docker-1.9.1"

main() {
  case $1 in
    start)
      start_docker
      ;;
    stop)
      stop_docker
      ;;
    restart)
      stop_docker
      start_docker
      ;;
    *)
      echo "usage: $0 (start|stop|restart)" >&2
      exit 1
      ;;
  esac
}

start_docker() {
  if ! [[ -s "${DOCKER}" ]]; then
    echo "Downloading Docker ${VERSION} to ${DOCKER}..."
    mkdir -p "$(dirname "${DOCKER}")"
    curl -fSL -o "${DOCKER}" "${URL}"
    if ! echo "${SHASUM}  ${DOCKER}" | sha256sum -c -; then
      rm "${DOCKER}"
      exit 1
    fi
    chmod +x "${DOCKER}"
  fi

  if sudo "${DOCKER}" version 2>/dev/null | grep -qF "${VERSION}"; then
    return
  fi

  sudo start-stop-daemon \
    --start \
    --background \
    --no-close \
    --pidfile "${PID}" \
    --make-pidfile \
    --exec "${DOCKER}" \
    -- \
    daemon \
    --group "$(id --group)" \
    &> "${LOG}"

  wait_docker
}

stop_docker() {
  sudo start-stop-daemon \
    --stop \
    --oknodo \
    --pidfile "${PID}"
  sudo rm -f "${PID}"
}

wait_docker() {
  for i in $(seq 100); do
    if "${DOCKER}" version &>/dev/null; then
      return
    fi
    sleep 0.1
  done

  ( echo "ERROR: docker did not start:"; cat "${LOG}" ) >&2
  exit 1
}

main "$@"
