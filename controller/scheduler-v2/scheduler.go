package main

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/flynn/flynn/Godeps/_workspace/src/gopkg.in/inconshreveable/log15.v2"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/controller/utils"
	"github.com/flynn/flynn/host/types"
)

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

type jobTypeMap map[string]map[jobKey]*Job

type jobKey struct {
	hostID, jobID string
}

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
	hosts, err := f.s.cluster.Hosts()
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
	h, err := f.s.cluster.Host(hostID)
	if err != nil {
		return nil, err
	}

	// TODO remove
	return nil, nil
	config := f.jobConfig(typ, h.ID())

	// Provision a data volume on the host if needed.
	if f.Release.Processes[typ].Data {
		if err := utils.ProvisionVolume(h, config); err != nil {
			return nil, err
		}
	}

	job = f.jobs.Add(typ, h.ID, config.ID)
	job.Formation = f
	f.c.jobs.Add(job)

	_, err = f.c.AddJobs(map[string][]*host.Job{h.ID: {config}})
	if err != nil {
		f.jobs.Remove(job)
		f.c.jobs.Remove(config.ID, h.ID)
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

func (f *Formation) listJobs(h utils.HostClient, jobType string) ([]*host.Job, error) {
	allJobs, err := h.ListJobs()
	if err != nil {
		return nil, err
	}
	if jobType == "" {
		return allJobs, nil
	} else {
		jobs := make([]*host.Job, len(allJobs))
		for jobID, job := range allJobs {
			if f.jobType(job) == jobType {
				jobs = append(jobs, job)
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

type Scheduler struct {
	cluster    utils.ClusterClient
	log        log15.Logger
	formations *Formations

	jobs    map[string]*host.Job
	jobsMtx sync.RWMutex

	listeners map[chan *Event]struct{}
	listenMtx sync.RWMutex

	stop     chan struct{}
	stopOnce sync.Once

	formationChange chan *ct.ExpandedFormation
}

func NewScheduler(cluster utils.ClusterClient) *Scheduler {
	return &Scheduler{
		cluster:    cluster,
		log:        log15.New("component", "scheduler"),
		jobs:       make(map[string]*host.Job),
		listeners:  make(map[chan *Event]struct{}),
		stop:       make(chan struct{}),
		formations: newFormations(),
	}
}

func main() {
	return
}

func (s *Scheduler) Run() error {
	log := s.log.New("fn", "Run")
	log.Info("starting scheduler loop")
	defer log.Info("exiting scheduler loop")

	for {
		// check if we should stop first
		select {
		case <-s.stop:
			return nil
		default:
		}

		log.Info("starting cluster sync")
		if err := s.Sync(); err != nil {
			log.Error("error performing cluster sync", "err", err)
			continue
		}
		log.Info("finished cluster sync")

		// TODO: watch events
		select {
		case <-s.stop:
			return nil
		case fc := <-s.formationChange:
			if err := s.FormationChange(fc); err != nil {
				log.Error("error performing cluster sync", "err", err)
				continue
			}
		case <-time.After(time.Second):
		}
	}
	return nil
}

func (s *Scheduler) Sync() (err error) {
	log := s.log.New("fn", "Sync")

	defer func() {
		log.Info("sending cluster-sync event")
		s.sendEvent(&Event{Type: EventTypeClusterSync, err: err})
	}()

	log.Info("getting host list")
	hosts, err := s.cluster.Hosts()
	if err != nil {
		log.Error("error getting host list", "err", err)
		return err
	}
	log.Info(fmt.Sprintf("got %d hosts", len(hosts)))

	s.jobsMtx.Lock()
	defer s.jobsMtx.Unlock()
	for _, h := range hosts {
		log = log.New("host_id", h.ID())
		log.Info("getting jobs list")
		jobs, err := h.ListJobs()
		if err != nil {
			log.Error("error getting jobs list", "err", err)
			continue
		}
		log.Info("got jobs", "count", len(jobs))
		for id, job := range jobs {
			log.Info("adding job", "job.id", id)
			s.jobs[job.ID] = job
		}
	}
	return err
}

func (s *Scheduler) FormationChange(ef *ct.ExpandedFormation) (err error) {
	log := s.log.New("fn", "FormationChange")

	defer func() {
		if err != nil {
			log.Error("error in FormationChange", "err", err)
		}
		log.Info("sending formation-change event")
		s.sendEvent(&Event{Type: EventTypeFormationChange, err: err})
	}()

	f := s.formations.Get(ef.App.ID, ef.Release.ID)
	if f != nil {
		f.SetProcesses(ef.Processes)
	} else {
		log.Info("creating new formation")
		f = NewFormation(s, ef)
		s.formations.Add(f)
	}
	return f.Rectify()
}

func (s *Scheduler) Stop() error {
	s.log.Info("stopping scheduler loop", "fn", "Stop")
	s.stopOnce.Do(func() { close(s.stop) })
	return nil
}

func (s *Scheduler) GetJob(id string) *host.Job {
	s.jobsMtx.RLock()
	defer s.jobsMtx.RUnlock()
	return s.jobs[id]
}

func (s *Scheduler) Subscribe(events chan *Event) *Stream {
	s.log.Info("adding subscriber", "fn", "Subscribe")
	s.listenMtx.Lock()
	defer s.listenMtx.Unlock()
	s.listeners[events] = struct{}{}
	return &Stream{s, events}
}

func (s *Scheduler) Unsubscribe(events chan *Event) {
	s.log.Info("removing subscriber", "fn", "Unsubscribe")
	s.listenMtx.Lock()
	defer s.listenMtx.Unlock()
	delete(s.listeners, events)
}

type Stream struct {
	s      *Scheduler
	events chan *Event
}

func (s *Stream) Close() error {
	s.s.Unsubscribe(s.events)
	return nil
}

func (s *Scheduler) sendEvent(event *Event) {
	s.listenMtx.RLock()
	defer s.listenMtx.RUnlock()
	s.log.Info(fmt.Sprintf("sending %q event to %d listeners", event.Type, len(s.listeners)))
	for ch := range s.listeners {
		// TODO: handle slow listeners
		ch <- event
	}
}

type Event struct {
	Type EventType
	err  error
}

type EventType string

const (
	EventTypeClusterSync     EventType = "cluster-sync"
	EventTypeFormationChange EventType = "formation-change"
)
