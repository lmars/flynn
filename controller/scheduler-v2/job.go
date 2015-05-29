package main

import (
	"sync"
	"time"
)

type jobKey struct {
	hostID, jobID string
}

type Job struct {
	ID        string
	HostID    string
	Type      string
	Formation *Formation

	restarts  int
	timer     *time.Timer
	timerMtx  sync.Mutex
	startedAt time.Time
}

func newJob(id, hostID, typ string) *Job {
	return &Job{ID: id, HostID: hostID, Type: typ}
}

type jobTypeMap map[string]map[jobKey]*Job

func (m jobTypeMap) Add(typ, host, id string) *Job {
	jobs, ok := m[typ]
	if !ok {
		jobs = make(map[jobKey]*Job)
		m[typ] = jobs
	}
	job := &Job{ID: id, HostID: host, Type: typ}
	jobs[jobKey{host, id}] = job
	return job
}

func (m jobTypeMap) Remove(job *Job) {
	if jobs, ok := m[job.Type]; ok {
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
