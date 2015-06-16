package main

import (
	"sync"
	"time"
)

type jobKey struct {
	hostID, jobID string
}

type JobRequestType string

const (
	JobRequestTypeUp   JobRequestType = "up"
	JobRequestTypeDown JobRequestType = "down"
)

type JobSpec struct {
	JobType   string
	AppID     string
	ReleaseID string
}

func NewJobSpec(typ, appID, releaseID string) *JobSpec {
	return &JobSpec{
		JobType:   typ,
		AppID:     appID,
		ReleaseID: releaseID,
	}
}

type HostJobSpec struct {
	*JobSpec
	HostID string
}

func NewHostJobSpec(typ, appID, releaseID, hostID string) *HostJobSpec {
	return &HostJobSpec{
		JobSpec: NewJobSpec(typ, appID, releaseID),
		HostID:  hostID,
	}
}

type JobRequest struct {
	*HostJobSpec
	RequestType JobRequestType
}

func NewJobRequest(requestType JobRequestType, typ, appID, releaseID, hostID string) *JobRequest {
	return &JobRequest{
		HostJobSpec: NewHostJobSpec(typ, appID, releaseID, hostID),
		RequestType: requestType,
	}
}

type Job struct {
	*HostJobSpec
	ID string

	restarts  int
	timer     *time.Timer
	timerMtx  sync.Mutex
	startedAt time.Time
}

func NewJob(typ, appID, releaseID, hostID, id string) *Job {
	return &Job{
		HostJobSpec: NewHostJobSpec(typ, appID, releaseID, hostID),
		ID:          id,
	}
}

type jobTypeMap map[string]map[jobKey]*Job

func (m jobTypeMap) Add(typ, appID, releaseID, hostID, id string) *Job {
	jobs, ok := m[typ]
	if !ok {
		jobs = make(map[jobKey]*Job)
		m[typ] = jobs
	}
	job := NewJob(typ, appID, releaseID, hostID, id)
	jobs[jobKey{hostID, id}] = job
	return job
}

func (m jobTypeMap) Remove(job *Job) {
	if jobs, ok := m[job.JobType]; ok {
		j := jobs[jobKey{job.HostID, job.ID}]
		// cancel job restarts
		j.timerMtx.Lock()
		if j.timer != nil {
			j.timer.Stop()
			j.timer = nil
		}
		j.timerMtx.Unlock()
		delete(jobs, jobKey{job.HostID, job.ID})
	}
}

func (m jobTypeMap) Get(typ, host, id string) *Job {
	return m[typ][jobKey{host, id}]
}

func newJobMap() *jobMap {
	return &jobMap{jobs: make(map[string]*Job)}
}

type jobMap struct {
	jobs map[string]*Job
	mtx  sync.RWMutex
}

func (m *jobMap) Add(job *Job) {
	m.mtx.Lock()
	m.jobs[job.ID] = job
	m.mtx.Unlock()
}

func (m *jobMap) Remove(job string) {
	m.mtx.Lock()
	delete(m.jobs, job)
	m.mtx.Unlock()
}

func (m *jobMap) Get(job string) *Job {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.jobs[job]
}

func (m *jobMap) Len() int {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return len(m.jobs)
}
