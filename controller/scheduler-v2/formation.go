package main

import (
	"fmt"
	"math"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/controller/utils"
	"github.com/flynn/flynn/host/types"
)

type Formations struct {
	formations map[utils.FormationKey]*Formation
}

func newFormations() *Formations {
	return &Formations{
		formations: make(map[utils.FormationKey]*Formation),
	}
}

func (fs *Formations) Get(appID, releaseID string) *Formation {
	if form, ok := fs.formations[utils.FormationKey{AppID: appID, ReleaseID: releaseID}]; ok {
		return form
	}
	return nil
}

func (fs *Formations) Add(f *Formation) *Formation {
	if existing, ok := fs.formations[f.key()]; ok {
		return existing
	}
	fs.formations[f.key()] = f
	return f
}

type Formation struct {
	*ct.ExpandedFormation

	jobs jobTypeMap
	s    *Scheduler
}

func NewFormation(s *Scheduler, ef *ct.ExpandedFormation) *Formation {
	return &Formation{
		ExpandedFormation: ef,
		jobs:              make(jobTypeMap),
		s:                 s,
	}
}

func (f *Formation) key() utils.FormationKey {
	return utils.FormationKey{f.App.ID, f.Release.ID}
}

func (f *Formation) SetFormation(ef *ct.ExpandedFormation) {
	f.ExpandedFormation = ef
}

func (f *Formation) Rectify() error {
	log := f.s.log.New("fn", "rectify")
	log.Info("rectifying formation", "app.id", f.App.ID, "release.id", f.Release.ID)

	for t, expected := range f.Processes {
		actual := len(f.jobs[t])
		diff := expected - actual
		if diff > 0 {
			f.sendJobRequest(JobRequestTypeUp, diff, t, "")
		} else if diff < 0 {
			f.sendJobRequest(JobRequestTypeDown, -diff, t, "")
		}
	}

	// remove extraneous process types
	for t, jobs := range f.jobs {
		// ignore jobs that don't have a type
		if t == "" {
			continue
		}

		if _, exists := f.Processes[t]; !exists {
			f.sendJobRequest(JobRequestTypeDown, len(jobs), t, "")
		}
	}
	return nil
}

func (f *Formation) sendJobRequest(requestType JobRequestType, numJobs int, typ string, hostID string) {
	for i := 0; i < numJobs; i++ {
		f.s.jobRequests <- NewJobRequest(requestType, typ, f.App.ID, f.Release.ID, hostID)
	}
}

func (f *Formation) handleJobRequest(requestType JobRequestType, typ string, hostID string) (err error) {
	log := f.s.log.New("fn", "handleJobRequest")
	defer func() {
		if err != nil {
			log.Error("error handling job request", "error", err)
		}
	}()

	switch requestType {
	case JobRequestTypeUp:
		_, err = f.startJob(typ, hostID)
	case JobRequestTypeDown:
	default:
		return fmt.Errorf("Unknown job request type")
	}
	return err
}

func (f *Formation) startJob(typ, hostID string) (job *Job, err error) {
	log := f.s.log.New("fn", "startJob")
	defer func() {
		if err != nil {
			log.Error("error starting job", "error", err)
		} else if job == nil {
			log.Error("couldn't obtain job", "job", job)
		} else {
			log.Info("started job", "host.id", job.HostID, "job.type", job.JobType, "job.id", job.ID)
		}
		f.s.sendEvent(NewEvent(EventTypeJobStart, err, job))
	}()
	h, err := f.findHost(typ, hostID)
	if err != nil {
		return nil, err
	}

	log.Info("formation", "app", f.App, "release", f.Release, "artifact", f.Artifact)
	hostJob := f.configureJob(typ, h.ID())

	// Provision a data volume on the host if needed.
	if f.Release.Processes[typ].Data {
		if err := utils.ProvisionVolume(h, hostJob); err != nil {
			return nil, err
		}
	}

	job = f.jobs.Add(typ, f.App.ID, f.Release.ID, h.ID(), hostJob.ID)
	f.s.jobs.Add(job)

	if err := h.AddJob(hostJob); err != nil {
		f.jobs.Remove(job)
		f.s.jobs.Remove(job.ID)
		return nil, err
	}
	f.s.PutJob(&ct.Job{
		ID:        h.ID() + "-" + job.ID,
		AppID:     job.AppID,
		ReleaseID: job.ReleaseID,
		Type:      job.JobType,
		State:     "up",
		Meta:      utils.JobMetaFromMetadata(hostJob.Metadata),
	})
	return job, nil
}

func (f *Formation) configureJob(typ, hostID string) *host.Job {
	return utils.JobConfig(&ct.ExpandedFormation{
		App:      &ct.App{ID: f.App.ID, Name: f.App.Name},
		Release:  f.Release,
		Artifact: f.Artifact,
	}, typ, hostID)
}

func (f *Formation) listJobs(h utils.HostClient, jobType string) (map[string]host.ActiveJob, error) {
	allJobs, err := h.ListJobs()
	if err != nil {
		return nil, err
	}
	if jobType == "" {
		return allJobs, nil
	} else {
		jobs := make(map[string]host.ActiveJob, len(allJobs))
		for jobID, job := range allJobs {
			if f.jobType(job.Job) == jobType {
				jobs[jobID] = job
			}
		}
		return jobs, nil
	}
}

func (f *Formation) findHost(typ, hostID string) (utils.HostClient, error) {
	log := f.s.log.New("fn", "findHost")
	hosts, err := f.s.Hosts()
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("scheduler: no online hosts")
	}

	var minCount int = math.MaxInt32
	if hostID == "" {
		for _, host := range hosts {
			jobs, err := f.listJobs(host, typ)
			if err != nil {
				log.Error("error listing jobs", "job.type", typ, "host.id", host.ID())
				continue
			}
			if len(jobs) < minCount {
				minCount = len(jobs)
				hostID = host.ID()
			}
		}
	}
	if hostID == "" {
		return nil, fmt.Errorf("no host found")
	}
	h, err := f.s.Host(hostID)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (f *Formation) jobType(job *host.Job) string {
	if job.Metadata["flynn-controller.app"] != f.App.ID ||
		job.Metadata["flynn-controller.release"] != f.Release.ID {
		return ""
	}
	return job.Metadata["flynn-controller.type"]
}
