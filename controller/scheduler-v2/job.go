package main

import (
	"sync"
	"time"
)

type jobKey struct {
	hostID, jobID string
}

type IJobSpec interface {
	Type() string
	AppID() string
	ReleaseID() string
}

type IJobRequest interface {
	IJobSpec
	HostID() string
}

type JobSpec struct {
	typ       string
	appID     string
	releaseID string
}

type JobRequest struct {
	IJobSpec
	hostID string
}

type Job struct {
	IJobRequest
	id string

	restarts  int
	timer     *time.Timer
	timerMtx  sync.Mutex
	startedAt time.Time
}

func (j *JobSpec) Type() string {
	return j.typ
}

func (j *JobSpec) AppID() string {
	return j.appID
}

func (j *JobSpec) ReleaseID() string {
	return j.releaseID
}

func (j *JobRequest) HostID() string {
	return j.hostID
}

func (j *Job) ID() string {
	return j.id
}

func NewJobSpec(typ, appID, releaseID string) *JobSpec {
	return &JobSpec{
		typ:       typ,
		appID:     appID,
		releaseID: releaseID,
	}
}

func NewJobRequest(typ, appID, releaseID, hostID string) *JobRequest {
	return &JobRequest{
		IJobSpec: NewJobSpec(typ, appID, releaseID),
		hostID:   hostID,
	}
}

func NewJob(typ, appID, releaseID, hostID, id string) *Job {
	return &Job{
		IJobRequest: NewJobRequest(typ, appID, releaseID, hostID),
		id:          id,
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
	if jobs, ok := m[job.Type()]; ok {
		j := jobs[jobKey{job.HostID(), job.ID()}]
		// cancel job restarts
		j.timerMtx.Lock()
		if j.timer != nil {
			j.timer.Stop()
			j.timer = nil
		}
		j.timerMtx.Unlock()
		delete(jobs, jobKey{job.HostID(), job.ID()})
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
	m.jobs[job.ID()] = job
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
