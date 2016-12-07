#!/bin/bash

set -eo pipefail

version="1.7.4"
shasum="47fda42e46b4c3ec93fa5d4d4cc6a748aa3f9411a2a2b7e08e3a6d80d753ec8b"
dir="/usr/local"

apt-get update
apt-get install --yes git build-essential
apt-get clean

curl -fsSLo /tmp/go.tar.gz "https://storage.googleapis.com/golang/go${version}.linux-amd64.tar.gz"
echo "${shasum}  /tmp/go.tar.gz" | shasum -c -
tar xzf /tmp/go.tar.gz -C "${dir}"
rm /tmp/go.tar.gz

export GOROOT="/usr/local/go"
export GOPATH="/go"
export PATH="${GOROOT}/bin:${PATH}"
go get github.com/jteeuwen/go-bindata/go-bindata
go get github.com/tools/godep
go get github.com/flynn/go-tuf/cmd/tuf
go get github.com/flynn/go-tuf/cmd/tuf-client

mkdir -p "${GOPATH}/src/github.com/flynn"
ln -nfs "$(pwd)" "${GOPATH}/src/github.com/flynn/flynn"

cp "builder/go-wrapper.sh" "/usr/local/bin/go"
cp "builder/go-wrapper.sh" "/usr/local/bin/cgo"
