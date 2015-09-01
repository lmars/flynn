package main

import (
	"encoding/json"
	"reflect"
	"time"

	. "github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-check"
	ct "github.com/flynn/flynn/controller/types"
	hh "github.com/flynn/flynn/pkg/httphelper"
)

func (s *S) TestCreateDeployment(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "create-deployment"})
	release := s.createTestRelease(c, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": {}},
	})
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID:     app.ID,
		ReleaseID: release.ID,
		Processes: map[string]int{"web": 1},
	}), IsNil)

	// deploying an initial release should no-op
	d, err := s.c.CreateDeployment(app.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(d.FinishedAt, NotNil)
	// but the app release should now be set
	gotRelease, err := s.c.GetAppRelease(app.ID)
	c.Assert(release.ID, Equals, gotRelease.ID)

	newRelease := s.createTestRelease(c, &ct.Release{})

	d, err = s.c.CreateDeployment(app.ID, newRelease.ID)
	c.Assert(err, IsNil)
	c.Assert(d.ID, Not(Equals), "")
	c.Assert(d.AppID, Equals, app.ID)
	c.Assert(d.NewReleaseID, Equals, newRelease.ID)
	c.Assert(d.OldReleaseID, Equals, release.ID)

	// quickly recreating a deployment should error
	_, err = s.c.CreateDeployment(app.ID, newRelease.ID)
	c.Assert(hh.IsValidationError(err), Equals, true)
	c.Assert(err.(hh.JSONError).Message, Equals, "Cannot create deploy, there is already one in progress for this app.")
}

func (s *S) TestStreamDeployment(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "stream-deployment"})
	release := s.createTestRelease(c, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": {}},
	})
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID:     app.ID,
		ReleaseID: release.ID,
		Processes: map[string]int{"web": 1},
	}), IsNil)
	c.Assert(s.c.SetAppRelease(app.ID, release.ID), IsNil)

	newRelease := s.createTestRelease(c, &ct.Release{})

	d, err := s.c.CreateDeployment(app.ID, newRelease.ID)
	c.Assert(err, IsNil)
	c.Assert(d.ID, Not(Equals), "")
	events := make(chan *ct.DeploymentEvent)
	stream, err := s.c.StreamDeployment(d, events)
	c.Assert(err, IsNil)
	defer stream.Close()

	// send fake event

	createDeploymentEvent := func(e ct.DeploymentEvent) {
		if e.Status == "" {
			e.Status = "running"
		}
		data, err := json.Marshal(e)
		c.Assert(err, IsNil)
		query := "INSERT INTO events (app_id, object_id, object_type, data) VALUES ($1, $2, $3, $4)"
		c.Assert(s.hc.db.Exec(query, app.ID, e.DeploymentID, string(ct.EventTypeDeployment), data), IsNil)
	}
	createDeploymentEvent(ct.DeploymentEvent{DeploymentID: d.ID, ReleaseID: newRelease.ID})

	select {
	case e, ok := <-events:
		if !ok {
			c.Fatal("unexpected close of event stream")
		}
		c.Assert(e.ReleaseID, Equals, newRelease.ID)
	case <-time.After(time.Second):
		c.Fatal("Timed out waiting for event")
	}
}

func (s *S) TestGetDeployment(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "get-deployment"})
	release := s.createTestRelease(c, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": {}},
	})
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID:     app.ID,
		ReleaseID: release.ID,
		Processes: map[string]int{"web": 1},
	}), IsNil)

	// deploy initial release
	d, err := s.c.CreateDeployment(app.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(d.Status, Equals, "complete")
	newRelease := s.createTestRelease(c, &ct.Release{})

	// create a second deployment
	d, err = s.c.CreateDeployment(app.ID, newRelease.ID)
	c.Assert(err, IsNil)
	c.Assert(d.Status, Equals, "pending")

	// test we can retrieve it
	deployment, err := s.c.GetDeployment(d.ID)
	c.Assert(err, IsNil)
	c.Assert(deployment.ID, Equals, d.ID)
	c.Assert(deployment.AppID, Equals, app.ID)
	c.Assert(deployment.OldReleaseID, Equals, release.ID)
	c.Assert(deployment.NewReleaseID, Equals, newRelease.ID)
	c.Assert(deployment.Status, Equals, d.Status)
	c.Assert(reflect.DeepEqual(deployment.Processes, map[string]int{"web": 1}), Equals, true)
}

func (s *S) TestDeploymentList(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "list-deployment"})
	release := s.createTestRelease(c, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": {}},
	})
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID:     app.ID,
		ReleaseID: release.ID,
		Processes: map[string]int{"web": 1},
	}), IsNil)

	// deploy initial release
	initial, err := s.c.CreateDeployment(app.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(initial.Status, Equals, "complete")
	newRelease := s.createTestRelease(c, &ct.Release{})

	// create a second deployment
	second, err := s.c.CreateDeployment(app.ID, newRelease.ID)
	c.Assert(second.Status, Equals, "pending")
	c.Assert(err, IsNil)

	// test we get back both the initial release and the new deployment
	deployments, err := s.c.DeploymentList(app.ID)
	c.Assert(err, IsNil)
	c.Assert(deployments, HasLen, 2)
	c.Assert(deployments[1].ID, Equals, initial.ID)
	c.Assert(deployments[0].ID, Equals, second.ID)
}
