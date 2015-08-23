package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"syscall"
	"time"

	c "github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-check"
	"github.com/flynn/flynn/host/types"
	logaggc "github.com/flynn/flynn/logaggregator/client"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/dialer"
	"github.com/flynn/flynn/pkg/exec"
)

// ZHostUpdateSuite is prefixed with "Z" so that it runs after all other tests
// due to the fact that the scheduler does not handle removing hosts very well.
//
// TODO: Remove this hack once the scheduler handles removing hosts from the
//       cluster (see https://github.com/flynn/flynn/issues/1484).
type ZHostUpdateSuite struct {
	Helper
}

var _ = c.Suite(&ZHostUpdateSuite{})

func (s *ZHostUpdateSuite) TestUpdateLogs(t *c.C) {
	if testCluster == nil {
		t.Skip("cannot boot new hosts")
	}

	instance := s.addHost(t)
	defer s.removeHost(t, instance)
	httpClient := &http.Client{Transport: &http.Transport{Dial: dialer.Retry.Dial}}
	client := cluster.NewHost(instance.ID, fmt.Sprintf("http://%s:1113", instance.IP), httpClient)

	// start partial logger job
	cmd := exec.JobUsingHost(
		client,
		exec.DockerImage(imageURIs["test-apps"]),
		&host.Job{
			Config: host.ContainerConfig{Cmd: []string{"/bin/partial-logger"}},
			Metadata: map[string]string{
				"flynn-controller.app": "partial-logger",
			},
		},
	)
	t.Assert(cmd.Start(), c.IsNil)
	defer cmd.Kill()

	// wait for partial line
	_, err := s.discoverdClient(t).Instances("partial-logger", 10*time.Second)
	t.Assert(err, c.IsNil)

	// update flynn-host
	pid, err := client.Update("/usr/local/bin/flynn-host", "daemon", "--id", cmd.HostID)
	t.Assert(err, c.IsNil)
	// update the pid file so removeHost works
	t.Assert(instance.Run(fmt.Sprintf("echo -n %d | sudo tee /var/run/flynn-host.pid", pid), nil), c.IsNil)

	// finish logging
	t.Assert(client.SignalJob(cmd.Job.ID, int(syscall.SIGUSR1)), c.IsNil)

	// check we get a single log line
	logc, err := logaggc.New("")
	t.Assert(err, c.IsNil)
	log, err := logc.GetLog("partial-logger", &logaggc.LogOpts{Follow: true})
	t.Assert(err, c.IsNil)
	defer log.Close()
	msgs := make(chan *logaggc.Message)
	go func() {
		defer close(msgs)
		dec := json.NewDecoder(log)
		for {
			var msg logaggc.Message
			if err := dec.Decode(&msg); err != nil {
				debugf(t, "error decoding message: %s", err)
				return
			}
			msgs <- &msg
		}
	}()
	for {
		select {
		case msg, ok := <-msgs:
			if !ok {
				t.Fatal("error getting log")
			}
			if msg.Stream == "stdout" {
				t.Assert(msg.Msg, c.Equals, "hello world")
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for log")
		}
	}
}
