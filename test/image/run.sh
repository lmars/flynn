#!/bin/bash

set -e

export HOME="/root"
export PATH="${ROOT}/cli/bin:${ROOT}/host/bin:${PATH}"
export BACKOFF_PERIOD="5s"

main() {
  if [[ -n "${ROUTER_IP}" ]] && [[ -n "${DOMAIN}" ]]; then
    echo "${ROUTER_IP}" \
      "${DOMAIN}" \
      "controller.${DOMAIN}" \
      "git.${DOMAIN}" \
      "dashboard.${DOMAIN}" \
      "docker.${DOMAIN}" \
      >> /etc/hosts
  fi

  export FLYNNRC="$(mktemp)"
  flynn cluster add --docker ${CLUSTER_ADD_ARGS}

  cd "${ROOT}/test"

  exec /usr/bin/timeout \
    --signal=QUIT \
    --kill-after=10 \
    45m \
    /bin/flynn-test \
    --flynnrc "${FLYNNRC}" \
    $@
}

main "$@"
