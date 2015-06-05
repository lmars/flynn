package main

import (
	"fmt"
	"testing"
	"time"

	. "github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-check"
	. "github.com/flynn/flynn/controller/testutils"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/types"
)

func Test(t *testing.T) { TestingT(t) }

type TestSuite struct{}

var _ = Suite(&TestSuite{})

func createTestScheduler(jobID string) *Scheduler {
	h := NewFakeHostClient("host-1")
	h.AddJob(&host.Job{
		ID: jobID,
		Metadata: map[string]string{
			"flynn-controller.app":     "testApp",
			"flynn-controller.release": "testRelease",
		},
	})
	cluster := NewFakeCluster()
	cluster.SetHosts(map[string]*FakeHostClient{h.ID(): h})
	cc := NewFakeControllerClient()

	return NewScheduler(cluster, cc)
}

func waitForEventType(events chan *Event, etype EventType) error {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("unexpected close of scheduler event stream")
			}
			if event.Type == etype {
				return nil
			}
		case <-time.After(time.Second):
			return fmt.Errorf("timed out waiting for cluster sync event")
		}
	}
}

func (ts *TestSuite) TestInitialClusterSync(c *C) {
	jobID := "job-1"
	s := createTestScheduler(jobID)
	s.log.Info("Testing cluster sync")

	events := make(chan *Event)
	stream := s.Subscribe(events)
	defer stream.Close()
	go s.Run()
	defer s.Stop()

	// wait for a cluster sync event
	err := waitForEventType(events, EventTypeClusterSync)
	fatalIfError(c, err)

	// check the scheduler has the job
	job, err := s.GetJob(jobID)
	c.Assert(err, IsNil)
	c.Assert(job, NotNil)
	c.Assert(job.Job.ID, Equals, jobID)
}

func (ts *TestSuite) TestFormationChange(c *C) {
	jobID := "job-1"

	s := createTestScheduler(jobID)
	s.log.Info("Testing formation change")

	events := make(chan *Event, 1)
	stream := s.Subscribe(events)
	defer stream.Close()
	go s.Run()
	defer s.Stop()

	// wait for a cluster sync event
	err := waitForEventType(events, EventTypeClusterSync)
	fatalIfError(c, err)

	s.formationChange <- &ct.ExpandedFormation{
		App: &ct.App{
			Name: "test-formation-change",
			ID:   "test-formation-change",
		},
		Release: &ct.Release{
			ID: "test-formation-change",
		},
	}

	err = waitForEventType(events, EventTypeFormationChange)
	fatalIfError(c, err)

	s.stop <- struct{}{}
}

func fatalIfError(c *C, err error) {
	if err != nil {
		c.Fatal(err.Error())
	}
}
