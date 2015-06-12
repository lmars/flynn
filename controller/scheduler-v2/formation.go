package main

import (
	"fmt"
	"math"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/controller/utils"
	"github.com/flynn/flynn/host/types"
)

type formationKey struct {
	appID, releaseID string
}

type Formations struct {
	formations map[formationKey]*Formation
}

func newFormations() *Formations {
	return &Formations{
		formations: make(map[formationKey]*Formation),
	}
}

func (fs *Formations) Get(appID, releaseID string) *Formation {
	if form, ok := fs.formations[formationKey{appID, releaseID}]; ok {
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

func (f *Formation) key() formationKey {
	return formationKey{f.App.ID, f.Release.ID}
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
			f.add(diff, t, "")
		} else if diff < 0 {
			f.remove(-diff, t, "")
		}
	}

	// remove extraneous process types
	for t, jobs := range f.jobs {
		// ignore jobs that don't have a type
		if t == "" {
			continue
		}

		if _, exists := f.Processes[t]; !exists {
			f.remove(len(jobs), t, "")
		}
	}
	return nil
}

func (f *Formation) add(n int, name string, hostID string) {
	log := f.s.log.New("fn", "add")
	log.Info("adding jobs", "count", n, "job.name", name, "host.id", hostID)
	for i := 0; i < n; i++ {
		f.start(name, hostID)
	}
}

func (f *Formation) remove(n int, name string, hostID string) {
	log := f.s.log.New("fn", "remove")
	log.Info("removing jobs", "count", n, "job.name", name, "host.id", hostID)
	// TODO implement
}

func (f *Formation) start(typ string, hostID string) (job *Job, err error) {
	log := f.s.log.New("fn", "remove")
	defer func() {
		if err != nil {
			log.Error("error starting job", "error", err)
		} else {
			log.Info("started job", "host.id", job.HostID, "job.id", job.ID)
		}
	}()
	h, err := f.findHost(typ, hostID)
	if err != nil {
		return nil, err
	}

	hostJob := f.jobConfig(typ, h.ID())

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
		f.s.jobs.Remove(job.ID())
		return nil, err
	}
	return job, nil
}

func (f *Formation) jobConfig(name string, hostID string) *host.Job {
	return utils.JobConfig(&ct.ExpandedFormation{
		App:      &ct.App{ID: f.App.ID, Name: f.App.Name},
		Release:  f.Release,
		Artifact: f.Artifact,
	}, name, hostID)
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
