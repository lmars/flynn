package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/template"

	c "github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-check"
	"github.com/flynn/flynn/controller/client"
	"github.com/flynn/flynn/pkg/pinned"
	tc "github.com/flynn/flynn/test/cluster"
)

type ReleaseSuite struct {
	Helper
}

var _ = c.ConcurrentSuite(&ReleaseSuite{})

func (s *ReleaseSuite) addReleaseHosts(t *c.C) *tc.BootResult {
	res, err := testCluster.AddReleaseHosts()
	t.Assert(err, c.IsNil)
	t.Assert(res.Instances, c.HasLen, 4)
	return res
}

var releaseScript = bytes.NewReader([]byte(`
export TUF_TARGETS_PASSPHRASE="flynn-test"
export TUF_SNAPSHOT_PASSPHRASE="flynn-test"
export TUF_TIMESTAMP_PASSPHRASE="flynn-test"

export GOPATH=~/go
src="${GOPATH}/src/github.com/flynn/flynn"

# send all output to stderr so only version.json is output to stdout
(

  # rebuild components.
  #
  # ideally we would use tup to do this, but it hangs waiting on the
  # FUSE socket after building, so for now we do it manually.
  #
  # See https://github.com/flynn/flynn/issues/949
  pushd "${src}" >/dev/null
  sed "s/{{TUF-ROOT-KEYS}}/$(tuf --dir test/release root-keys)/g" host/cli/root_keys.go.tmpl > host/cli/root_keys.go
  vpkg="github.com/flynn/flynn/pkg/version"
  go build -o host/bin/flynn-host -ldflags="-X ${vpkg}.commit dev -X ${vpkg}.branch dev -X ${vpkg}.tag v20150131.0-test -X ${vpkg}.dirty false" ./host
  gzip -9 --keep --force host/bin/flynn-host
  sed "s/{{FLYNN-HOST-CHECKSUM}}/$(sha512sum host/bin/flynn-host.gz | cut -d " " -f 1)/g" script/install-flynn.tmpl > script/install-flynn

  # create new images
  for name in $(docker images | grep ^flynn | awk '{print $1}'); do
    docker build -t $name - < <(echo -e "FROM $name\nRUN /bin/true")
  done

  util/release/flynn-release manifest util/release/version_template.json > version.json
  popd >/dev/null

  "${src}/script/export-components" "${src}/test/release"

  dir=$(mktemp --directory)
  ln -s "${src}/test/release/repository" "${dir}/tuf"
  ln -s "${src}/script/install-flynn" "${dir}/install-flynn"

  # start a blobstore to serve the exported components
  sudo start-stop-daemon \
    --start \
    --background \
    --exec "${src}/blobstore/bin/flynn-blobstore" \
    -- \
    -d=false \
    -s="${dir}" \
    -p=8080
) >&2

cat "${src}/version.json"
`))

var installScript = template.Must(template.New("install-script").Parse(`
# download to a tmp file so the script fails on download error rather than
# executing nothing and succeeding
curl -sL --fail http://{{ .Blobstore }}/install-flynn > /tmp/install-flynn
bash -e /tmp/install-flynn -r "http://{{ .Blobstore }}"
`))

var updateScript = template.Must(template.New("update-script").Parse(`
export GOPATH="$(mktemp --directory)"
go get github.com/flynn/go-tuf/cmd/tuf-client
sudo mv "${GOPATH}/bin/tuf-client" /usr/bin/tuf-client

tuf-client init --store /tmp/tuf.db http://{{ .Blobstore }}/tuf <<< '{{ .RootKeys }}'
flynn-host update --repository http://{{ .Blobstore }}/tuf --tuf-db /tmp/tuf.db
`))

func (s *ReleaseSuite) TestReleaseImages(t *c.C) {
	if testCluster == nil {
		t.Skip("cannot boot release cluster")
	}

	// stream script output to t.Log
	logReader, logWriter := io.Pipe()
	go func() {
		buf := bufio.NewReader(logReader)
		for {
			line, err := buf.ReadString('\n')
			debug(t, line[0:len(line)-1])
			if err != nil {
				return
			}
		}
	}()

	// boot the release cluster, release components to a blobstore and output the new version.json
	releaseCluster := s.addReleaseHosts(t)
	buildHost := releaseCluster.Instances[0]
	var versionJSON bytes.Buffer
	t.Assert(buildHost.Run("bash -ex", &tc.Streams{Stdin: releaseScript, Stdout: &versionJSON, Stderr: logWriter}), c.IsNil)
	var versions map[string]string
	t.Assert(json.Unmarshal(versionJSON.Bytes(), &versions), c.IsNil)

	// install Flynn from the blobstore on the vanilla host
	// installHost := releaseCluster.Instances[3]
	// var script bytes.Buffer
	// installScript.Execute(&script, map[string]string{"Blobstore": buildHost.IP + ":8080"})
	// var installOutput bytes.Buffer
	// out := io.MultiWriter(logWriter, &installOutput)
	// t.Assert(installHost.Run("sudo bash -ex", &tc.Streams{Stdin: &script, Stdout: out, Stderr: out}), c.IsNil)

	// // check the flynn-host version is correct
	// var hostVersion bytes.Buffer
	// t.Assert(installHost.Run("flynn-host version", &tc.Streams{Stdout: &hostVersion}), c.IsNil)
	// t.Assert(strings.TrimSpace(hostVersion.String()), c.Equals, "v20150131.0-test")

	// // check rebuilt images were downloaded
	// for name, id := range versions {
	// 	expected := fmt.Sprintf("%s image %s downloaded", name, id)
	// 	if !strings.Contains(installOutput.String(), expected) {
	// 		t.Fatalf(`expected install to download %s %s`, name, id)
	// 	}
	// }

	// run a cluster update from the blobstore
	var rootKeys bytes.Buffer
	t.Assert(buildHost.Run("tuf --dir ~/go/src/github.com/flynn/flynn/test/release root-keys", &tc.Streams{Stdout: &rootKeys}), c.IsNil)
	updateHost := releaseCluster.Instances[1]
	// script = bytes.Buffer{}
	var script bytes.Buffer
	updateScript.Execute(&script, map[string]string{"Blobstore": buildHost.IP + ":8080", "RootKeys": rootKeys.String()})
	var updateOutput bytes.Buffer
	// out = io.MultiWriter(logWriter, &updateOutput)
	out := io.MultiWriter(logWriter, &updateOutput)
	t.Assert(updateHost.Run("bash -ex", &tc.Streams{Stdin: &script, Stdout: out, Stderr: out}), c.IsNil)

	// check rebuilt images were downloaded
	for name, id := range versions {
		for _, host := range releaseCluster.Instances[0:2] {
			expected := fmt.Sprintf(`"finished pulling image" host=%s name=%s id=%s`, host.ID, name, id)
			if !strings.Contains(updateOutput.String(), expected) {
				t.Fatalf(`expected update to download %s %s on host %s`, name, id, host.ID)
			}
		}
	}

	// create a controller client for the new cluster
	pin, err := base64.StdEncoding.DecodeString(releaseCluster.ControllerPin)
	t.Assert(err, c.IsNil)
	client, err := controller.NewClientWithPinConfig(
		"https://"+buildHost.IP,
		releaseCluster.ControllerKey,
		&pinned.Config{Pin: pin, Config: &tls.Config{ServerName: releaseCluster.ControllerDomain}},
	)
	t.Assert(err, c.IsNil)
	client.Host = releaseCluster.ControllerDomain

	// check system apps were deployed correctly
	systemApps := []string{"blobstore", "dashboard", "router", "gitreceive", "controller"}
	for _, app := range systemApps {
		debugf(t, "checking system app: %s", app)
		expected := fmt.Sprintf(`"system app deployed" name=%s`, app)
		if !strings.Contains(updateOutput.String(), expected) {
			t.Fatalf(`expected update to deploy %s`, app)
		}
		_, err := client.GetApp(app)
		t.Assert(err, c.IsNil)
		release, err := client.GetAppRelease(app)
		t.Assert(err, c.IsNil)
		artifact, err := client.GetArtifact(release.ArtifactID)
		t.Assert(err, c.IsNil)
		image := "flynn/" + app
		if app == "gitreceive" {
			image = "flynn/receiver"
		}
		uri, err := url.Parse(artifact.URI)
		t.Assert(err, c.IsNil)
		t.Assert(uri.Query().Get("id"), c.Equals, versions[image])
	}
}
