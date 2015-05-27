package main

import (
	"fmt"
	"testing"
	"time"

	. "github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-check"
	. "github.com/flynn/flynn/controller/testutils"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/host/volume"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/random"
	"github.com/flynn/flynn/pkg/stream"
)

func Test(t *testing.T) { TestingT(t) }

type TestSuite struct{}

var _ = Suite(&TestSuite{})

type FakeHost struct {
	id      string
	jobs    map[string]host.ActiveJob
	volumes map[string]*volume.Info
	stream  stream.Stream
}

func (f *FakeHost) ID() string {
	return f.id
}

func (f *FakeHost) ListJobs() (map[string]host.ActiveJob, error) {
	return f.jobs, nil
}

func (f *FakeHost) GetJob(id string) (*host.ActiveJob, error) {
	return &f.jobs[id], nil
}

func (f *FakeHost) StopJob(id string) error {
	j, ok := &f.jobs[id]
	if !ok {
		return fmt.Errorf("Could not locate job to delete with id %q", id)
	}
	delete(f.jobs, id)
	return nil
}

func (f *FakeHost) SignalJob(id string, sig int) error {
	return nil
}

func (f *FakeHost) StreamEvents(id string, ch chan<- *host.Event) (stream.Stream, error) {
	return f.stream, nil
}

func (f *FakeHost) CreateVolume(providerId string) (*volume.Info, error) {
	vol := &volume.Info{ID: random.UUID()}
	f.volumes[vol.ID] = vol
	return vol, nil
}

func (f *FakeHost) DestroyVolume(volumeID string) error {
	j, ok := f.volumes[volumeID]
	if !ok {
		return fmt.Errorf("Could not locate volume to delete with id %q", id)
	}
	delete(f.volumes, volumeID)
	return nil
}

func (f *FakeHost) CreateSnapshot(volumeID string) (*volume.Info, error) {
	var res volume.Info
	err := c.c.Put(fmt.Sprintf("/storage/volumes/%s/snapshot", volumeID), nil, &res)
	return &res, err
}

func (f *FakeHost) PullSnapshot(receiveVolID string, sourceHostID string, sourceSnapID string) (*volume.Info, error) {
	var res volume.Info
	pull := volume.PullCoordinate{
		HostID:     sourceHostID,
		SnapshotID: sourceSnapID,
	}
	err := c.c.Post(fmt.Sprintf("/storage/volumes/%s/pull_snapshot", receiveVolID), pull, &res)
	return &res, err
}

func (f *FakeHost) SendSnapshot(snapID string, assumeHaves []json.RawMessage) (io.ReadCloser, error) {
	header := http.Header{
		"Accept": []string{"application/vnd.zfs.snapshot-stream"},
	}
	res, err := c.c.RawReq("GET", fmt.Sprintf("/storage/volumes/%s/send", snapID), header, assumeHaves, nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

func (f *FakeHost) PullImages(repository, driver, root string, tufDB io.Reader, ch chan<- *layer.PullInfo) (stream.Stream, error) {
	header := http.Header{"Content-Type": {"application/octet-stream"}}
	path := fmt.Sprintf("/host/pull-images?repository=%s&driver=%s&root=%s", repository, driver, root)
	return c.c.StreamWithHeader("POST", path, header, tufDB, ch)
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
