package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/flynn/flynn/Godeps/_workspace/src/gopkg.in/inconshreveable/log15.v2"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/controller/utils"
	"github.com/flynn/flynn/host/types"
)

const eventBufferSize int = 1000

type Scheduler struct {
	utils.ControllerClient
	utils.ClusterClient
	log        log15.Logger
	formations *Formations

	jobs    *jobMap
	jobsMtx sync.RWMutex

	listeners map[chan Event]struct{}
	listenMtx sync.RWMutex

	stop     chan struct{}
	stopOnce sync.Once

	formationChange chan *ct.ExpandedFormation
	jobRequests     chan *JobRequest
}

func NewScheduler(cluster utils.ClusterClient, cc utils.ControllerClient) *Scheduler {
	return &Scheduler{
		ControllerClient: cc,
		ClusterClient:    cluster,
		log:              log15.New("component", "scheduler"),
		jobs:             newJobMap(),
		listeners:        make(map[chan Event]struct{}),
		stop:             make(chan struct{}),
		formations:       newFormations(),
		formationChange:  make(chan *ct.ExpandedFormation, 1),
		jobRequests:      make(chan *JobRequest, eventBufferSize),
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
		// first, check if we should stop or process pending job events
		select {
		case <-s.stop:
			return nil
		case req := <-s.jobRequests:
			s.HandleJobRequest(req)
			continue
		default:
		}

		log.Info("starting cluster sync")
		if err := s.Sync(); err != nil {
			log.Error("error performing cluster sync", "err", err)
			continue
		}

		log.Info("starting watching events")
		select {
		case <-s.stop:
			return nil
		case fc := <-s.formationChange:
			if err := s.FormationChange(fc); err != nil {
				log.Error("error performing formation change", "err", err)
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
		s.sendEvent(NewEvent(EventTypeClusterSync, err, nil))
	}()

	log.Info("getting host list")
	hosts, err := s.Hosts()
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
		activeJobs, err := h.ListJobs()
		if err != nil {
			log.Error("error getting jobs list", "err", err)
			continue
		}
		log.Info("got jobs", "count", len(activeJobs))
		for _, activeJob := range activeJobs {
			job := activeJob.Job
			appID := job.Metadata["flynn-controller.app"]
			appName := job.Metadata["flynn-controller.app_name"]
			releaseID := job.Metadata["flynn-controller.release"]
			jobType := job.Metadata["flynn-controller.type"]
			log.Info("adding job", "host.id", h.ID(), "job.id", job.ID, "app.id", appID, "release.id", releaseID, "type", jobType)

			if appID == "" || releaseID == "" {
				continue
			}
			if job := s.jobs.Get(job.ID); job != nil {
				continue
			}

			f, err := s.getFormation(appID, appName, releaseID)
			if err != nil {
				continue
			}
			// TODO finish creating job
			s.PutJob(&ct.Job{
				ID:        h.ID() + "-" + job.ID,
				AppID:     appID,
				ReleaseID: releaseID,
				Type:      jobType,
				State:     "up",
				Meta:      utils.JobMetaFromMetadata(job.Metadata),
			})
			j := f.jobs.Add(jobType, appID, releaseID, h.ID(), job.ID)
			s.jobs.Add(j)
		}
	}
	return err
}

func (s *Scheduler) getFormation(appID, appName, releaseID string) (*Formation, error) {
	log := s.log.New("fn", "getFormation")

	artifacts := make(map[string]*ct.Artifact)
	releases := make(map[string]*ct.Release)

	f := s.formations.Get(appID, releaseID)
	if f == nil {
		release := releases[releaseID]
		var err error
		if release == nil {
			release, err = s.GetRelease(releaseID)
			if err != nil {
				log.Error("at", "getRelease", "status", "error", "err", err)
				return nil, err
			}
			releases[release.ID] = release
		}

		artifact := artifacts[release.ArtifactID]
		if artifact == nil {
			artifact, err := s.GetArtifact(release.ArtifactID)
			if err != nil {
				log.Error("at", "getArtifact", "status", "error", "err", err)
				return nil, err
			}
			artifacts[artifact.ID] = artifact
		}

		formation, err := s.GetFormation(appID, releaseID)
		if err != nil {
			log.Error("at", "getFormation", "status", "error", "err", err)
			return nil, err
		}

		f = NewFormation(s, &ct.ExpandedFormation{
			App:       &ct.App{ID: appID, Name: appName},
			Release:   release,
			Artifact:  artifact,
			Processes: formation.Processes,
		})
		log.Info("at", "addFormation")
		f = s.formations.Add(f)
	}
	if f == nil {
		return nil, fmt.Errorf("no formation found")
	}
	return f, nil
}

func (s *Scheduler) FormationChange(ef *ct.ExpandedFormation) (err error) {
	log := s.log.New("fn", "FormationChange")

	defer func() {
		if err != nil {
			log.Error("error in FormationChange", "err", err)
		}
		s.sendEvent(NewEvent(EventTypeFormationChange, err, nil))
	}()

	f := s.formations.Get(ef.App.ID, ef.Release.ID)
	if f != nil {
		f.SetFormation(ef)
	} else {
		log.Info("creating new formation")
		f = NewFormation(s, ef)
		s.formations.Add(f)
	}
	return f.Rectify()
}

func (s *Scheduler) HandleJobRequest(req *JobRequest) error {
	f := s.formations.Get(req.AppID, req.ReleaseID)
	f.handleJobRequest(req.RequestType, req.JobType, req.HostID)
	return nil
}

func (s *Scheduler) Stop() error {
	s.log.Info("stopping scheduler loop", "fn", "Stop")
	s.stopOnce.Do(func() { close(s.stop) })
	return nil
}

func (s *Scheduler) GetJob(id string) (*host.ActiveJob, error) {
	s.jobsMtx.RLock()
	defer s.jobsMtx.RUnlock()
	job := s.jobs.Get(id)
	if job == nil {
		return nil, fmt.Errorf("No job found with ID %q", id)
	}
	host, err := s.Host(job.HostID)
	if err != nil {
		return nil, err
	}
	hostJob, err := host.GetJob(id)
	if err != nil {
		return nil, err
	}
	return hostJob, nil
}

func (s *Scheduler) Subscribe(events chan Event) *Stream {
	s.log.Info("adding subscriber", "fn", "Subscribe")
	s.listenMtx.Lock()
	defer s.listenMtx.Unlock()
	s.listeners[events] = struct{}{}
	return &Stream{s, events}
}

func (s *Scheduler) Unsubscribe(events chan Event) {
	s.log.Info("removing subscriber", "fn", "Unsubscribe")
	s.listenMtx.Lock()
	defer s.listenMtx.Unlock()
	delete(s.listeners, events)
}

type Stream struct {
	s      *Scheduler
	events chan Event
}

func (s *Stream) Close() error {
	s.s.Unsubscribe(s.events)
	return nil
}

func (s *Scheduler) sendEvent(event Event) {
	s.listenMtx.RLock()
	defer s.listenMtx.RUnlock()
	s.log.Info("sending event to listeners", "event.type", event.Type(), "listeners.count", len(s.listeners))
	for ch := range s.listeners {
		// TODO: handle slow listeners
		ch <- event
	}
}

type Event interface {
	Type() EventType
	Err() error
}

type EventType string

const (
	EventTypeDefault         EventType = "default"
	EventTypeClusterSync     EventType = "cluster-sync"
	EventTypeFormationChange EventType = "formation-change"
	EventTypeJobStart        EventType = "start-job"
)

type DefaultEvent struct {
	err error
	typ EventType
}

func (de *DefaultEvent) Err() error {
	return de.err
}

func (de *DefaultEvent) Type() EventType {
	return de.typ
}

type ClusterSyncEvent struct {
	Event
}

type FormationChangeEvent struct {
	Event
}

type JobStartEvent struct {
	Event
	Job *Job
}

func NewEvent(typ EventType, err error, data interface{}) Event {
	switch typ {
	case EventTypeClusterSync:
		return &ClusterSyncEvent{Event: &DefaultEvent{err: err, typ: typ}}
	case EventTypeFormationChange:
		return &FormationChangeEvent{Event: &DefaultEvent{err: err, typ: typ}}
	case EventTypeJobStart:
		job, ok := data.(*Job)
		if !ok {
			job = nil
		}
		return &JobStartEvent{Event: &DefaultEvent{err: err, typ: typ}, Job: job}
	default:
		return &DefaultEvent{err: err, typ: EventTypeDefault}
	}
}
