package main

import (
	"fmt"
	"testing"
	"time"

	. "github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-check"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/types"
)

func Test(t *testing.T) { TestingT(t) }

type TestSuite struct{}

var _ = Suite(&TestSuite{})

type FakeCluster struct {
	hosts []Host
}

func (f *FakeCluster) ListHosts() ([]Host, error) {
	return f.hosts, nil
}

type FakeHost struct {
	id   string
	jobs map[string]host.ActiveJob
}

func (f *FakeHost) ID() string {
	return f.id
}

func (f *FakeHost) ListJobs() (map[string]host.ActiveJob, error) {
	return f.jobs, nil
}

func createTestScheduler(jobID string) *Scheduler {
	host := &FakeHost{id: "host-1", jobs: map[string]host.ActiveJob{
		jobID: {Job: &host.Job{ID: jobID}},
	}}
	cluster := &FakeCluster{hosts: []Host{host}}

	return NewScheduler(cluster)
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

	events := make(chan *Event)
	stream := s.Subscribe(events)
	defer stream.Close()
	go s.Run()
	defer s.Stop()

	// wait for a cluster sync event
	err := waitForEventType(events, EventTypeClusterSync)
	if err != nil {
		c.Fatal(err.Error())
	}

	// check the scheduler has the job
	job := s.GetJob(jobID)
	c.Assert(job, NotNil)
	c.Assert(job.Job.ID, Equals, jobID)
}

func (ts *TestSuite) TestFormationChange(c *C) {
	jobID := "job-1"

	s := createTestScheduler(jobID)

	events := make(chan *Event)
	stream := s.Subscribe(events)
	defer stream.Close()
	go s.Run()
	defer s.Stop()

	// wait for a cluster sync event
	err := waitForEventType(events, EventTypeClusterSync)
	fatalIfError(c, err)

	err = s.FormationChange(&ct.ExpandedFormation{
		App: &ct.App{
			Name: "test-formation-change",
			ID:   "test-formation-change",
		},
		Release: &ct.Release{
			ID: "test-formation-change",
		},
	})
	fatalIfError(c, err)

	err = waitForEventType(events, EventTypeFormationChange)
	fatalIfError(c, err)
}

func fatalIfError(c *C, err error) {
	if err != nil {
		c.Fatal(err.Error())
	}
}
