package server_test

import (
	"io/ioutil"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/discoverd/server"
	"github.com/flynn/flynn/pkg/stream"
)

// Ensure the store can open and close.
func TestStore_Open(t *testing.T) {
	s := NewStore()
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// Ensure the store returns an error when opening without a bind address.
func TestStore_Open_ErrBindAddressRequired(t *testing.T) {
	s := NewStore()
	s.BindAddress = ""
	if err := s.Open(); err != server.ErrBindAddressRequired {
		t.Fatal(err)
	}
}

// Ensure the store returns an error when opening without an advertised address.
func TestStore_Open_ErrAdvertiseRequired(t *testing.T) {
	s := NewStore()
	s.Advertise = nil
	if err := s.Open(); err != server.ErrAdvertiseRequired {
		t.Fatal(err)
	}
}

// Ensure the store can add a service.
func TestStore_AddService(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()

	// Add a service.
	if err := s.AddService("service0", &discoverd.ServiceConfig{LeaderType: discoverd.LeaderTypeManual}); err != nil {
		t.Fatal(err)
	}

	// Validate that the data has been applied.
	if c := s.Config("service0"); !reflect.DeepEqual(c, &discoverd.ServiceConfig{LeaderType: discoverd.LeaderTypeManual}) {
		t.Fatalf("unexpected config: %#v", c)
	}
}

// Ensure the store uses a default config if one is not specified.
func TestStore_AddService_DefaultConfig(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()

	// Add a service.
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	}

	// Validate that the data has been applied.
	if c := s.Config("service0"); !reflect.DeepEqual(c, &discoverd.ServiceConfig{LeaderType: discoverd.LeaderTypeOldest}) {
		t.Fatalf("unexpected config: %#v", c)
	}
}

// Ensure the store returns an error when creating a service that already exists.
func TestStore_AddService_ErrServiceExists(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()

	// Add a service twice.
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AddService("service0", nil); !server.IsServiceExists(err) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Ensure the store can remove an existing service.
func TestStore_RemoveService(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()

	// Add services.
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	} else if err = s.AddService("service1", nil); err != nil {
		t.Fatal(err)
	} else if err = s.AddService("service2", nil); err != nil {
		t.Fatal(err)
	}

	// Remove one service.
	if err := s.RemoveService("service1"); err != nil {
		t.Fatal(err)
	}

	// Validate that only two services remain.
	if a := s.ServiceNames(); !reflect.DeepEqual(a, []string{"service0", "service2"}) {
		t.Fatalf("unexpected services: %+v", a)
	}
}

// Ensure the store sends down events when removing a service.
func TestStore_RemoveService_Events(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	} else if err = s.AddInstance("service0", &discoverd.Instance{ID: "inst0"}); err != nil {
		t.Fatal(err)
	} else if err = s.AddInstance("service0", &discoverd.Instance{ID: "inst1"}); err != nil {
		t.Fatal(err)
	}

	// Add subscription.
	ch := make(chan *discoverd.Event, 2)
	s.Subscribe("service0", false, discoverd.EventKindDown, ch)

	// Remove service.
	if err := s.RemoveService("service0"); err != nil {
		t.Fatal(err)
	}

	// Verify two down events were received.
	if e := <-ch; !reflect.DeepEqual(e, &discoverd.Event{Service: "service0", Kind: discoverd.EventKindDown, Instance: &discoverd.Instance{ID: "inst0", Index: 3}}) {
		t.Fatalf("unexpected event(0): %#v", e)
	}
	if e := <-ch; !reflect.DeepEqual(e, &discoverd.Event{Service: "service0", Kind: discoverd.EventKindDown, Instance: &discoverd.Instance{ID: "inst1", Index: 4}}) {
		t.Fatalf("unexpected event(1): %#v", e)
	}
}

// Ensure the store returns an error when removing non-existent services.
func TestStore_RemoveService_ErrNotFound(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.RemoveService("no_such_service"); !server.IsNotFound(err) {
		t.Fatalf("unexpected error: %s", err)
	}
}

// Ensure the store can add instances to a service.
func TestStore_AddInstance(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	}

	// Add an instances to the service.
	if err := s.AddInstance("service0", &discoverd.Instance{ID: "inst0"}); err != nil {
		t.Fatal(err)
	} else if err = s.AddInstance("service0", &discoverd.Instance{ID: "inst1"}); err != nil {
		t.Fatal(err)
	}

	// Verify that the instances exist.
	if a := s.Instances("service0"); !reflect.DeepEqual(a, []*discoverd.Instance{
		{ID: "inst0", Index: 3},
		{ID: "inst1", Index: 4},
	}) {
		t.Fatalf("unexpected instances: %#v", a)
	}
}

// Ensure the store can add instances to a service.
func TestStore_AddInstance_ErrNotFound(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddInstance("no_such_service", &discoverd.Instance{ID: "inst0"}); !server.IsNotFound(err) {
		t.Fatalf("unexpected error: %s", err)
	}
}

// Ensure the store sends an "up" event when adding a new service.
func TestStore_AddInstance_UpEvent(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	}

	// Add subscription.
	ch := make(chan *discoverd.Event, 1)
	s.Subscribe("service0", false, discoverd.EventKindUp, ch)

	// Add instance.
	if err := s.AddInstance("service0", &discoverd.Instance{ID: "inst0"}); err != nil {
		t.Fatal(err)
	}

	// Verify "up" event was received.
	if e := <-ch; !reflect.DeepEqual(e, &discoverd.Event{
		Service:  "service0",
		Kind:     discoverd.EventKindUp,
		Instance: &discoverd.Instance{ID: "inst0", Index: 3},
	}) {
		t.Fatalf("unexpected event: %#v", e)
	}
}

// Ensure the store sends an "update" event when updating an existing service.
func TestStore_AddInstance_UpdateEvent(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	} else if err = s.AddInstance("service0", &discoverd.Instance{ID: "inst0", Proto: "http"}); err != nil {
		t.Fatal(err)
	}

	// Add subscription.
	ch := make(chan *discoverd.Event, 1)
	s.Subscribe("service0", false, discoverd.EventKindUpdate, ch)

	// Update instance.
	if err := s.AddInstance("service0", &discoverd.Instance{ID: "inst0", Proto: "https"}); err != nil {
		t.Fatal(err)
	}

	// Verify "update" event was received.
	if e := <-ch; !reflect.DeepEqual(e, &discoverd.Event{
		Service:  "service0",
		Kind:     discoverd.EventKindUpdate,
		Instance: &discoverd.Instance{ID: "inst0", Proto: "https"},
	}) {
		t.Fatalf("unexpected event: %#v", e)
	}
}

// Ensure the store sends a "leader" event when adding the first instance.
func TestStore_AddInstance_LeaderEvent(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	}

	// Add subscription.
	ch := make(chan *discoverd.Event, 1)
	s.Subscribe("service0", false, discoverd.EventKindLeader, ch)

	// Update instance.
	if err := s.AddInstance("service0", &discoverd.Instance{ID: "inst0"}); err != nil {
		t.Fatal(err)
	}

	// Verify "leader" event was received.
	if e := <-ch; !reflect.DeepEqual(e, &discoverd.Event{
		Service:  "service0",
		Kind:     discoverd.EventKindLeader,
		Instance: &discoverd.Instance{ID: "inst0", Index: 3},
	}) {
		t.Fatalf("unexpected event: %#v", e)
	}
}

// Ensure the store can remove an instance from a service.
func TestStore_RemoveInstance(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	} else if err = s.AddInstance("service0", &discoverd.Instance{ID: "inst0"}); err != nil {
		t.Fatal(err)
	} else if err = s.AddInstance("service0", &discoverd.Instance{ID: "inst1"}); err != nil {
		t.Fatal(err)
	} else if err = s.AddInstance("service0", &discoverd.Instance{ID: "inst2"}); err != nil {
		t.Fatal(err)
	}

	// Remove one instance.
	if err := s.RemoveInstance("service0", "inst1"); err != nil {
		t.Fatal(err)
	}

	// Verify the remaining instances.
	if a := s.Instances("service0"); !reflect.DeepEqual(a, []*discoverd.Instance{
		{ID: "inst0", Index: 3},
		{ID: "inst2", Index: 5},
	}) {
		t.Fatalf("unexpected instances: %#v", a)
	}
}

// Ensure the store returns an error when removing an instance from a non-existent service.
func TestStore_RemoveInstance_ErrNotFound(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.RemoveInstance("no_such_service", "inst0"); !server.IsNotFound(err) {
		t.Fatalf("unexpected error: %s", err)
	}
}

// Ensure the store sends a "down" event when removing an existing service.
func TestStore_RemoveInstance_DownEvent(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	} else if err = s.AddInstance("service0", &discoverd.Instance{ID: "inst0"}); err != nil {
		t.Fatal(err)
	}

	// Add subscription.
	ch := make(chan *discoverd.Event, 1)
	s.Subscribe("service0", false, discoverd.EventKindDown, ch)

	// Remove instance.
	if err := s.RemoveInstance("service0", "inst0"); err != nil {
		t.Fatal(err)
	}

	// Verify "down" event was received.
	if e := <-ch; !reflect.DeepEqual(e, &discoverd.Event{
		Service:  "service0",
		Kind:     discoverd.EventKindDown,
		Instance: &discoverd.Instance{ID: "inst0", Index: 3},
	}) {
		t.Fatalf("unexpected event: %#v", e)
	}
}

// Ensure the store sends a "leader" event when removing an existing service.
func TestStore_RemoveInstance_LeaderEvent(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", &discoverd.ServiceConfig{LeaderType: discoverd.LeaderTypeOldest}); err != nil {
		t.Fatal(err)
	} else if err = s.AddInstance("service0", &discoverd.Instance{ID: "inst0"}); err != nil {
		t.Fatal(err)
	} else if err = s.AddInstance("service0", &discoverd.Instance{ID: "inst1"}); err != nil {
		t.Fatal(err)
	}

	// Add subscription.
	ch := make(chan *discoverd.Event, 1)
	s.Subscribe("service0", false, discoverd.EventKindLeader, ch)

	// Remove instance, inst1 should become leader.
	if err := s.RemoveInstance("service0", "inst0"); err != nil {
		t.Fatal(err)
	}

	// Verify "leader" event was received.
	if e := <-ch; !reflect.DeepEqual(e, &discoverd.Event{
		Service:  "service0",
		Kind:     discoverd.EventKindLeader,
		Instance: &discoverd.Instance{ID: "inst1", Index: 4},
	}) {
		t.Fatalf("unexpected event: %#v", e)
	}
}

// Ensure the store can store meta data for a service.
func TestStore_SetServiceMeta_Create(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	}

	// Set metadata.
	if err := s.SetServiceMeta("service0", &discoverd.ServiceMeta{Data: []byte(`"foo"`), Index: 0}); err != nil {
		t.Fatal(err)
	}

	// Verify metadata was updated.
	if m := s.ServiceMeta("service0"); !reflect.DeepEqual(m, &discoverd.ServiceMeta{Data: []byte(`"foo"`), Index: 3}) {
		t.Fatalf("unexpected meta: %#v", m)
	}
}

// Ensure the store returns an error when creating service meta that already exists.
func TestStore_SetServiceMeta_Create_ErrObjectExists(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	} else if err = s.SetServiceMeta("service0", &discoverd.ServiceMeta{Data: []byte(`"foo"`), Index: 0}); err != nil {
		t.Fatal(err)
	}

	// Create metadata with index=0.
	if err := s.SetServiceMeta("service0", &discoverd.ServiceMeta{Data: []byte(`"bar"`), Index: 0}); err == nil || err.Error() != `object_exists: Service metadata for "service0" already exists, use index=n to set` {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Ensure the store can update existing meta data for a service.
func TestStore_SetServiceMeta_Update(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	} else if err = s.SetServiceMeta("service0", &discoverd.ServiceMeta{Data: []byte(`"foo"`), Index: 0}); err != nil {
		t.Fatal(err)
	}

	// Update using previous index.
	if err := s.SetServiceMeta("service0", &discoverd.ServiceMeta{Data: []byte(`"bar"`), Index: 3}); err != nil {
		t.Fatal(err)
	}

	// Verify metadata was updated.
	if m := s.ServiceMeta("service0"); !reflect.DeepEqual(m, &discoverd.ServiceMeta{Data: []byte(`"bar"`), Index: 4}) {
		t.Fatalf("unexpected meta: %#v", m)
	}
}

// Ensure the store returns an error when updating a non-existent service meta.
func TestStore_SetServiceMeta_Update_ErrPreconditionFailed_Create(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	}

	// Update metadata with index>0.
	if err := s.SetServiceMeta("service0", &discoverd.ServiceMeta{Data: []byte(`"foo"`), Index: 100}); err == nil || err.Error() != `precondition_failed: Service metadata for "service0" does not exist, use index=0 to set` {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Ensure the store returns an error when updating service meta with the wrong CAS index.
func TestStore_SetServiceMeta_Update_ErrPreconditionFailed_WrongIndex(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	} else if err = s.SetServiceMeta("service0", &discoverd.ServiceMeta{Data: []byte(`"foo"`), Index: 0}); err != nil {
		t.Fatal(err)
	}

	// Update metadata with wrong previous index.
	if err := s.SetServiceMeta("service0", &discoverd.ServiceMeta{Data: []byte(`"bar"`), Index: 100}); err == nil || err.Error() != `precondition_failed: Service metadata for "service0" exists, but wrong index provided` {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Ensure the store returns an error when setting metadata for a non-existent service.
func TestStore_SetServiceMeta_ErrNotFound(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.SetServiceMeta("service0", &discoverd.ServiceMeta{Data: []byte(`"foo"`), Index: 0}); !server.IsNotFound(err) {
		t.Fatalf("unexpected error: %s", err)
	}
}

// Ensure the store can manually set a leader for a manual service.
func TestStore_SetLeader(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", &discoverd.ServiceConfig{LeaderType: discoverd.LeaderTypeManual}); err != nil {
		t.Fatal(err)
	} else if err := s.AddInstance("service0", &discoverd.Instance{ID: "inst0"}); err != nil {
		t.Fatal(err)
	} else if err := s.AddInstance("service0", &discoverd.Instance{ID: "inst1"}); err != nil {
		t.Fatal(err)
	}

	// Set the leader instance ID.
	if err := s.SetServiceLeader("service0", "inst1"); err != nil {
		t.Fatal(err)
	}

	// Verify that the leader was set.
	if inst := s.ServiceLeader("service0"); !reflect.DeepEqual(inst, &discoverd.Instance{ID: "inst1", Index: 4}) {
		t.Fatalf("unexpected leader: %#v", inst)
	}
}

// Ensure the store does not error when setting a leader for a non-existent service.
func TestStore_SetLeader_NoService(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.SetServiceLeader("service0", "inst1"); err != nil {
		t.Fatal(err)
	} else if inst := s.ServiceLeader("service0"); inst != nil {
		t.Fatalf("unexpected leader: %#v", inst)
	}
}

// Ensure the store does not error when setting a leader for a non-existent instance.
func TestStore_SetLeader_NoInstance(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", &discoverd.ServiceConfig{LeaderType: discoverd.LeaderTypeManual}); err != nil {
		t.Fatal(err)
	}

	if err := s.SetServiceLeader("service0", "inst1"); err != nil {
		t.Fatal(err)
	} else if inst := s.ServiceLeader("service0"); inst != nil {
		t.Fatalf("unexpected leader: %#v", inst)
	}
}

// Ensure the store removes blocking subscriptions.
func TestStore_Subscribe_NoBlock(t *testing.T) {
	s := MustOpenStore()
	defer s.Close()
	if err := s.AddService("service0", nil); err != nil {
		t.Fatal(err)
	}

	// Add blocking subscription.
	ch := make(chan *discoverd.Event, 0)
	s.Subscribe("service0", false, discoverd.EventKindUp, ch)

	// Add service.
	if err := s.AddInstance("service0", &discoverd.Instance{ID: "inst0"}); err != nil {
		t.Fatal(err)
	}

	// Ensure that program does not hang.
}

// Store represents a test wrapper for server.Store.
type Store struct {
	*server.Store
}

// NewStore returns a new instance of Store.
func NewStore() *Store {
	// Generate a temporary path.
	f, _ := ioutil.TempFile("", "discoverd-store-")
	f.Close()
	os.Remove(f.Name())

	// Initialize store.
	s := &Store{Store: server.NewStore(f.Name())}

	// Set default test settings.
	s.BindAddress = ":20000"
	s.Advertise, _ = net.ResolveTCPAddr("tcp", "localhost:20000")
	s.HeartbeatTimeout = 50 * time.Millisecond
	s.ElectionTimeout = 50 * time.Millisecond
	s.LeaderLeaseTimeout = 50 * time.Millisecond
	s.CommitTimeout = 5 * time.Millisecond
	s.EnableSingleNode = true

	// Turn off logs if verbose flag is not set.
	if !testing.Verbose() {
		s.LogOutput = ioutil.Discard
	}

	return s
}

// MustOpenStore returns a new, open instance of Store. Panic on error.
func MustOpenStore() *Store {
	s := NewStore()
	if err := s.Open(); err != nil {
		panic(err)
	}

	// Wait for leadership.
	<-s.LeaderCh()

	return s
}

// Close closes the store and removes its path.
func (s *Store) Close() error {
	defer os.RemoveAll(s.Path())
	return s.Store.Close()
}

// MockStore represents a mock implementation of Handler.Store.
type MockStore struct {
	LeaderFn           func() string
	AddPeerFn          func(peer string) error
	RemovePeerFn       func(peer string) error
	AddServiceFn       func(service string, config *discoverd.ServiceConfig) error
	RemoveServiceFn    func(service string) error
	SetServiceMetaFn   func(service string, meta *discoverd.ServiceMeta) error
	ServiceMetaFn      func(service string) *discoverd.ServiceMeta
	AddInstanceFn      func(service string, inst *discoverd.Instance) error
	RemoveInstanceFn   func(service, id string) error
	InstancesFn        func(service string) []*discoverd.Instance
	ConfigFn           func(service string) *discoverd.ServiceConfig
	SetServiceLeaderFn func(service, id string) error
	ServiceLeaderFn    func(service string) *discoverd.Instance
	SubscribeFn        func(service string, sendCurrent bool, kinds discoverd.EventKind, ch chan *discoverd.Event) stream.Stream
}

func (s *MockStore) Leader() string { return s.LeaderFn() }

func (s *MockStore) AddPeer(peer string) error    { return s.AddPeerFn(peer) }
func (s *MockStore) RemovePeer(peer string) error { return s.RemovePeerFn(peer) }

func (s *MockStore) AddService(service string, config *discoverd.ServiceConfig) error {
	return s.AddServiceFn(service, config)
}

func (s *MockStore) RemoveService(service string) error {
	return s.RemoveServiceFn(service)
}

func (s *MockStore) SetServiceMeta(service string, meta *discoverd.ServiceMeta) error {
	return s.SetServiceMetaFn(service, meta)
}

func (s *MockStore) ServiceMeta(service string) *discoverd.ServiceMeta {
	return s.ServiceMetaFn(service)
}

func (s *MockStore) AddInstance(service string, inst *discoverd.Instance) error {
	return s.AddInstanceFn(service, inst)
}

func (s *MockStore) RemoveInstance(service, id string) error {
	return s.RemoveInstanceFn(service, id)
}

func (s *MockStore) Instances(service string) []*discoverd.Instance {
	return s.InstancesFn(service)
}

func (s *MockStore) Config(service string) *discoverd.ServiceConfig {
	return s.ConfigFn(service)
}

func (s *MockStore) SetServiceLeader(service, id string) error {
	return s.SetServiceLeaderFn(service, id)
}

func (s *MockStore) ServiceLeader(service string) *discoverd.Instance {
	return s.ServiceLeaderFn(service)
}

func (s *MockStore) Subscribe(service string, sendCurrent bool, kinds discoverd.EventKind, ch chan *discoverd.Event) stream.Stream {
	return s.SubscribeFn(service, sendCurrent, kinds, ch)
}
