package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/boltdb/bolt"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/cluster"
)

// TODO: prune old jobs?

type State struct {
	id string

	jobs map[string]*host.ActiveJob
	mtx  sync.RWMutex

	containers map[string]*host.ActiveJob              // container ID -> job
	listeners  map[string]map[chan host.Event]struct{} // job id -> listener list (ID "all" gets all events)
	listenMtx  sync.RWMutex
	attachers  map[string]map[chan struct{}]struct{}

	stateFilePath string
	stateDB       *bolt.DB
	dbMtx         sync.RWMutex

	backend Backend
}

func NewState(id string, stateFilePath string) *State {
	return &State{
		id:            id,
		stateFilePath: stateFilePath,
		jobs:          make(map[string]*host.ActiveJob),
		containers:    make(map[string]*host.ActiveJob),
		listeners:     make(map[string]map[chan host.Event]struct{}),
		attachers:     make(map[string]map[chan struct{}]struct{}),
	}
}

/*
	Restore prior state from the save location defined at construction time.
	If the state save file is empty, nothing is loaded, and no error is returned.
*/
func (s *State) Restore(backend Backend) (func(), error) {
	s.dbMtx.RLock()
	defer s.dbMtx.RUnlock()
	if s.stateDB == nil {
		return nil, ErrDBClosed
	}

	s.backend = backend

	var resurrect []*host.ActiveJob
	if err := s.stateDB.View(func(tx *bolt.Tx) error {
		jobsBucket := tx.Bucket([]byte("jobs"))
		backendJobsBucket := tx.Bucket([]byte("backend-jobs"))
		backendGlobalBucket := tx.Bucket([]byte("backend-global"))
		resurrectionBucket := tx.Bucket([]byte("resurrection-jobs"))

		// restore jobs
		if err := jobsBucket.ForEach(func(k, v []byte) error {
			job := &host.ActiveJob{}
			if err := json.Unmarshal(v, job); err != nil {
				return err
			}
			if job.ContainerID != "" {
				s.containers[job.ContainerID] = job
			}
			s.jobs[string(k)] = job

			return nil
		}); err != nil {
			return err
		}

		// hand opaque blobs back to backend so it can do its restore
		backendJobsBlobs := make(map[string][]byte)
		if err := backendJobsBucket.ForEach(func(k, v []byte) error {
			backendJobsBlobs[string(k)] = v
			return nil
		}); err != nil {
			return err
		}
		backendGlobalBlob := backendGlobalBucket.Get([]byte("backend"))
		if err := backend.UnmarshalState(s.jobs, backendJobsBlobs, backendGlobalBlob); err != nil {
			return err
		}

		if resurrectionBucket == nil {
			s.mtx.Lock()
			for _, job := range s.jobs {
				// if there was an unclean shutdown, we resurrect all jobs marked
				// that were running at shutdown and are no longer running.
				if job.Job.Resurrect && job.Status != host.StatusRunning {
					resurrect = append(resurrect, job)
				}
			}
			s.mtx.Unlock()
		} else {
			defer tx.DeleteBucket([]byte("resurrection-jobs"))
			if err := resurrectionBucket.ForEach(func(k, v []byte) error {
				job := &host.ActiveJob{}
				if err := json.Unmarshal(v, job); err != nil {
					return err
				}
				resurrect = append(resurrect, job)
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil && err != io.EOF {
		return nil, fmt.Errorf("could not restore from host persistence db: %s", err)
	}

	return func() {
		var wg sync.WaitGroup
		wg.Add(len(resurrect))
		for _, job := range resurrect {
			go func(job *host.ActiveJob) {
				// generate a new job id, this is a new job
				newID := cluster.GenerateJobID(s.id)
				log.Printf("resurrecting %s as %s", job.Job.ID, newID)
				job.Job.ID = newID
				config := &RunConfig{
					// TODO(titanous): Use Job instead of ActiveJob in
					// resurrection bucket once InternalIP is not used.
					// TODO(titanous): Passing the IP is a hack, remove it once the
					// postgres appliance doesn't use it to calculate its ID in the
					// state machine.
					IP: net.ParseIP(job.InternalIP),
				}
				backend.Run(job.Job, config)
				wg.Done()
			}(job)
		}
		wg.Wait()
	}, nil
}

// MarkForResurrection is run during a clean shutdown and persists all running
// jobs with the resurrection flag before they are terminated by
// backend cleanup.
func (s *State) MarkForResurrection() error {
	s.dbMtx.RLock()
	defer s.dbMtx.RUnlock()
	if s.stateDB == nil {
		return nil
	}

	s.mtx.Lock()
	defer s.mtx.Unlock()
	return s.stateDB.Update(func(tx *bolt.Tx) error {
		tx.DeleteBucket([]byte("resurrection-jobs"))
		bucket, err := tx.CreateBucket([]byte("resurrection-jobs"))
		if err != nil {
			return err
		}

		for _, job := range s.jobs {
			if !job.Job.Resurrect || job.Status != host.StatusRunning {
				continue
			}
			data, err := json.Marshal(job)
			if err != nil {
				continue
			}
			if err := bucket.Put([]byte(job.Job.ID), data); err != nil {
				return err
			}
		}
		return nil
	})
}

// OpenDB opens and initialises the persistence DB, if not already open.
func (s *State) OpenDB() error {
	s.dbMtx.Lock()
	defer s.dbMtx.Unlock()

	if s.stateDB != nil {
		return nil
	}

	// open/initialize db
	if err := os.MkdirAll(filepath.Dir(s.stateFilePath), 0755); err != nil {
		return fmt.Errorf("could not not mkdir for db: %s", err)
	}
	stateDB, err := bolt.Open(s.stateFilePath, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return fmt.Errorf("could not open db: %s", err)
	}
	s.stateDB = stateDB
	if err := s.stateDB.Update(func(tx *bolt.Tx) error {
		// idempotently create buckets.  (errors ignored because they're all compile-time impossible args checks.)
		tx.CreateBucketIfNotExists([]byte("jobs"))
		tx.CreateBucketIfNotExists([]byte("backend-jobs"))
		tx.CreateBucketIfNotExists([]byte("backend-global"))
		return nil
	}); err != nil {
		return fmt.Errorf("could not initialize host persistence db: %s", err)
	}
	return nil
}

// CloseDB closes the persistence DB.
//
// The DB mutex is locked to protect s.stateDB, but also prevents closing the
// DB when it could still be needed to service API requests (see LockDB).
func (s *State) CloseDB() error {
	s.dbMtx.Lock()
	defer s.dbMtx.Unlock()
	if s.stateDB == nil {
		return nil
	}
	if err := s.stateDB.Close(); err != nil {
		return err
	}
	s.stateDB = nil
	return nil
}

var ErrDBClosed = errors.New("state DB closed")

// LockDB acquires a read lock on the DB mutex so that it cannot be closed
// until the caller has finished performing actions which will lead to changes
// being persisted to the DB.
//
// For example, running a job starts the job and then persists the change of
// state, but if the DB is closed in that time then the state of the running
// job will be lost.
//
// ErrDBClosed is returned if the DB is already closed so API requests will
// fail before any actions are performed.
func (s *State) LockDB() error {
	s.dbMtx.RLock()
	if s.stateDB == nil {
		s.dbMtx.RUnlock()
		return ErrDBClosed
	}
	return nil
}

// UnlockDB releases a read lock on the DB mutex, previously acquired by a call
// to LockDB.
func (s *State) UnlockDB() {
	s.dbMtx.RUnlock()
}

func (s *State) persist(jobID string) {
	// s.mtx.RLock() should already be covered by caller

	if err := s.stateDB.Update(func(tx *bolt.Tx) error {
		jobsBucket := tx.Bucket([]byte("jobs"))
		backendJobsBucket := tx.Bucket([]byte("backend-jobs"))
		backendGlobalBucket := tx.Bucket([]byte("backend-global"))

		// serialize the changed job, and push it into jobs bucket
		if _, exists := s.jobs[jobID]; exists {
			b, err := json.Marshal(s.jobs[jobID])
			if err != nil {
				return fmt.Errorf("failed to serialize job state: %s", err)
			}
			err = jobsBucket.Put([]byte(jobID), b)
			if err != nil {
				return fmt.Errorf("could not persist job to boltdb: %s", err)
			}
		} else {
			jobsBucket.Delete([]byte(jobID))
		}

		// save the opaque blob the backend provides regarding this job
		if backend, ok := s.backend.(JobStateSaver); ok {
			if _, exists := s.jobs[jobID]; exists {
				backendState, err := backend.MarshalJobState(jobID)
				if err != nil {
					return fmt.Errorf("backend failed to serialize job state: %s", err)
				}
				if backendState == nil {
					backendJobsBucket.Delete([]byte(jobID))
				} else {
					err = backendJobsBucket.Put([]byte(jobID), backendState)
					if err != nil {
						return fmt.Errorf("could not persist backend job state to boltdb: %s", err)
					}
				}
			} else {
				backendJobsBucket.Delete([]byte(jobID))
			}
		}

		// (re)save any state the backend provides that isn't tied to specific jobs.
		if backend, ok := s.backend.(StateSaver); ok {
			bytes, err := backend.MarshalGlobalState()
			if err != nil {
				return fmt.Errorf("backend failed to serialize global state: %s", err)
			}
			err = backendGlobalBucket.Put([]byte("backend"), bytes)
			if err != nil {
				return fmt.Errorf("could not persist backend global state to boltdb: %s", err)
			}
		}

		return nil
	}); err != nil {
		panic(fmt.Errorf("could not persist to boltdb: %s", err))
	}
}

func (s *State) AddJob(j *host.Job, ip net.IP) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	job := &host.ActiveJob{Job: j, HostID: s.id}
	if len(ip) > 0 {
		job.InternalIP = ip.String()
	}
	s.jobs[j.ID] = job
	s.sendEvent(job, "create")
	s.persist(j.ID)
}

func (s *State) GetJob(id string) *host.ActiveJob {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	job := s.jobs[id]
	if job == nil {
		return nil
	}
	jobCopy := *job
	return &jobCopy
}

func (s *State) RemoveJob(jobID string) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	delete(s.jobs, jobID)
	s.persist(jobID)
}

func (s *State) Get() map[string]host.ActiveJob {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	res := make(map[string]host.ActiveJob, len(s.jobs))
	for k, v := range s.jobs {
		res[k] = *v
	}
	return res
}

func (s *State) ClusterJobs() []*host.Job {
	s.mtx.RLock()
	defer s.mtx.RUnlock()

	res := make([]*host.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		res = append(res, j.Job)
	}
	return res
}

func (s *State) SetContainerID(jobID, containerID string) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.jobs[jobID].ContainerID = containerID
	s.containers[containerID] = s.jobs[jobID]
	s.persist(jobID)
}

func (s *State) SetForceStop(jobID string) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	job, ok := s.jobs[jobID]
	if !ok {
		return
	}

	job.ForceStop = true
	s.persist(jobID)
}

func (s *State) SetStatusRunning(jobID string) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	job, ok := s.jobs[jobID]
	if !ok || job.Status != host.StatusStarting {
		return
	}

	job.StartedAt = time.Now().UTC()
	job.Status = host.StatusRunning
	s.sendEvent(job, "start")
	if err := s.LockDB(); err == nil {
		s.persist(jobID)
		s.UnlockDB()
	}
}

func (s *State) SetContainerStatusDone(containerID string, exitCode int) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	job, ok := s.containers[containerID]
	if !ok {
		return
	}
	s.setStatusDone(job, exitCode)
}

func (s *State) SetStatusDone(jobID string, exitCode int) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		fmt.Println("SKIP")
		return
	}
	s.setStatusDone(job, exitCode)
}

func (s *State) setStatusDone(job *host.ActiveJob, exitStatus int) {
	if job.Status == host.StatusDone || job.Status == host.StatusCrashed || job.Status == host.StatusFailed {
		return
	}
	job.EndedAt = time.Now().UTC()
	job.ExitStatus = exitStatus
	if exitStatus == 0 {
		job.Status = host.StatusDone
	} else {
		job.Status = host.StatusCrashed
	}
	s.sendEvent(job, "stop")
	if err := s.LockDB(); err == nil {
		s.persist(job.Job.ID)
		s.UnlockDB()
	}
}

func (s *State) SetStatusFailed(jobID string, err error) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	job, ok := s.jobs[jobID]
	if !ok || job.Status == host.StatusDone || job.Status == host.StatusCrashed || job.Status == host.StatusFailed {
		return
	}
	job.Status = host.StatusFailed
	job.EndedAt = time.Now().UTC()
	errStr := err.Error()
	job.Error = &errStr
	s.sendEvent(job, "error")
	if err := s.LockDB(); err == nil {
		s.persist(jobID)
		s.UnlockDB()
	}
	go s.WaitAttach(jobID)
}

func (s *State) AddAttacher(jobID string, ch chan struct{}) *host.ActiveJob {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if job, ok := s.jobs[jobID]; ok {
		jobCopy := *job
		return &jobCopy
	}
	if _, ok := s.attachers[jobID]; !ok {
		s.attachers[jobID] = make(map[chan struct{}]struct{})
	}
	s.attachers[jobID][ch] = struct{}{}
	return nil
}

func (s *State) RemoveAttacher(jobID string, ch chan struct{}) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if a, ok := s.attachers[jobID]; ok {
		delete(a, ch)
		if len(a) == 0 {
			delete(s.attachers, jobID)
		}
	}
}

func (s *State) WaitAttach(jobID string) {
	s.mtx.Lock()
	a := s.attachers[jobID]
	delete(s.attachers, jobID)
	s.mtx.Unlock()
	for ch := range a {
		// signal attach
		ch <- struct{}{}
		// wait for attach
		<-ch
	}
}

func (s *State) AddListener(jobID string) chan host.Event {
	ch := make(chan host.Event)
	s.listenMtx.Lock()
	if _, ok := s.listeners[jobID]; !ok {
		s.listeners[jobID] = make(map[chan host.Event]struct{})
	}
	s.listeners[jobID][ch] = struct{}{}
	s.listenMtx.Unlock()
	return ch
}

func (s *State) RemoveListener(jobID string, ch chan host.Event) {
	go func() {
		// drain to prevent deadlock while removing the listener
		for range ch {
		}
	}()
	s.listenMtx.Lock()
	delete(s.listeners[jobID], ch)
	if len(s.listeners[jobID]) == 0 {
		delete(s.listeners, jobID)
	}
	s.listenMtx.Unlock()
	close(ch)
}

func (s *State) sendEvent(job *host.ActiveJob, event string) {
	j := *job
	go func() {
		s.listenMtx.RLock()
		defer s.listenMtx.RUnlock()
		e := host.Event{JobID: job.Job.ID, Job: &j, Event: event}
		for ch := range s.listeners["all"] {
			ch <- e
		}
		for ch := range s.listeners[job.Job.ID] {
			ch <- e
		}
	}()
}
