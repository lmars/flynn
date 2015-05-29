package main

import (
	"fmt"
	"math"
	"sync"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/controller/utils"
	"github.com/flynn/flynn/host/types"
)

type formationKey struct {
	appID, releaseID string
}

type Formations struct {
	formations map[formationKey]*Formation
	mtx        sync.RWMutex
}

func newFormations() *Formations {
	return &Formations{
		formations: make(map[formationKey]*Formation),
	}
}

func (fs *Formations) Get(appID, releaseID string) *Formation {
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()
	if form, ok := fs.formations[formationKey{appID, releaseID}]; ok {
		return form
	}
	return nil
}

func (fs *Formations) Add(f *Formation) *Formation {
	fs.mtx.Lock()
	defer fs.mtx.Unlock()
	if existing, ok := fs.formations[f.key()]; ok {
		return existing
	}
	fs.formations[f.key()] = f
	return f
}

type Formation struct {
	mtx       sync.Mutex
	AppID     string
	AppName   string
	Release   *ct.Release
	Artifact  *ct.Artifact
	Processes map[string]int

	jobs jobTypeMap
	s    *Scheduler
}

func NewFormation(s *Scheduler, ef *ct.ExpandedFormation) *Formation {
	return &Formation{
		AppID:     ef.App.ID,
		AppName:   ef.App.Name,
		Release:   ef.Release,
		Artifact:  ef.Artifact,
		Processes: ef.Processes,
		jobs:      make(jobTypeMap),
		s:         s,
	}
}

func (f *Formation) key() formationKey {
	return formationKey{f.AppID, f.Release.ID}
}

func (f *Formation) SetProcesses(p map[string]int) {
	f.mtx.Lock()
	f.Processes = p
	f.mtx.Unlock()
}

func (f *Formation) Rectify() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return f.rectify()
}

func (f *Formation) rectify() (err error) {
	log := f.s.log.New("fn", "rectify")
	log.Info("rectifying formation", "app.id", f.AppID, "release.id", f.Release.ID)

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
	hosts, err := f.s.Hosts()
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("scheduler: no online hosts")
	}

	if hostID == "" {
		var minCount int = math.MaxInt32
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

	hostJob := f.jobConfig(typ, h.ID())

	// Provision a data volume on the host if needed.
	if f.Release.Processes[typ].Data {
		if err := utils.ProvisionVolume(h, hostJob); err != nil {
			return nil, err
		}
	}

	job = f.jobs.Add(typ, h.ID(), hostJob.ID)
	job.Formation = f
	f.s.jobs.Add(job)

	if err := h.AddJob(hostJob); err != nil {
		f.jobs.Remove(job)
		f.s.jobs.Remove(job.ID)
		return nil, err
	}
	return job, nil
}

func (f *Formation) jobConfig(name string, hostID string) *host.Job {
	return utils.JobConfig(&ct.ExpandedFormation{
		App:      &ct.App{ID: f.AppID, Name: f.AppName},
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

func (f *Formation) jobType(job *host.Job) string {
	if job.Metadata["flynn-controller.app"] != f.AppID ||
		job.Metadata["flynn-controller.release"] != f.Release.ID {
		return ""
	}
	return job.Metadata["flynn-controller.type"]
}
