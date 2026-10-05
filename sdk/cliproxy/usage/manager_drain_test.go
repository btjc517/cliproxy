package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// gatedPlugin blocks inside the first delivery until gate is closed, so a test
// can hold records in the queue for as long as it needs.
type gatedPlugin struct {
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once

	mu        sync.Mutex
	delivered int
}

func newGatedPlugin() *gatedPlugin {
	return &gatedPlugin{gate: make(chan struct{}), entered: make(chan struct{})}
}

func (p *gatedPlugin) HandleUsage(context.Context, Record) {
	p.once.Do(func() { close(p.entered) })
	<-p.gate
	p.mu.Lock()
	p.delivered++
	p.mu.Unlock()
}

func (p *gatedPlugin) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.delivered
}

// startBlocked queues records on a new manager and returns once the
// dispatcher is stuck inside the plugin on the first of them.
func startBlocked(t *testing.T, records int) (*Manager, *gatedPlugin) {
	t.Helper()
	m := NewManager(0)
	plugin := newGatedPlugin()
	m.Register(plugin)
	for i := 0; i < records; i++ {
		m.Publish(context.Background(), Record{RequestID: "r"})
	}
	<-plugin.entered
	return m, plugin
}

func TestStopAndWaitDeliversEveryQueuedRecord(t *testing.T) {
	m, plugin := startBlocked(t, 3)
	result := make(chan int, 1)
	go func() {
		if err := m.StopAndWait(context.Background()); err != nil {
			t.Errorf("StopAndWait() error = %v, want nil", err)
		}
		result <- plugin.count()
	}()
	close(plugin.gate)
	if got := <-result; got != 3 {
		t.Fatalf("records delivered when StopAndWait returned = %d, want 3", got)
	}
}

func TestStopAndWaitWaitsWhileAPluginIsBusy(t *testing.T) {
	m, plugin := startBlocked(t, 2)
	defer close(plugin.gate)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- m.StopAndWait(ctx) }()
	// The dispatcher cannot finish while the plugin is blocked, so the only
	// way StopAndWait can return is through the cancelled context.
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("StopAndWait() error = %v, want context.Canceled", err)
	}
}

func TestStopAndWaitWithoutStartReturnsAtOnce(t *testing.T) {
	m := NewManager(0)
	if err := m.StopAndWait(context.Background()); err != nil {
		t.Fatalf("StopAndWait() error = %v, want nil", err)
	}
}

func TestPublishAfterStopAndWaitIsDropped(t *testing.T) {
	m := NewManager(0)
	plugin := newGatedPlugin()
	close(plugin.gate)
	m.Register(plugin)
	if err := m.StopAndWait(context.Background()); err != nil {
		t.Fatalf("StopAndWait() error = %v, want nil", err)
	}
	m.Publish(context.Background(), Record{RequestID: "late"})
	if err := m.StopAndWait(context.Background()); err != nil {
		t.Fatalf("second StopAndWait() error = %v, want nil", err)
	}
	if got := plugin.count(); got != 0 {
		t.Fatalf("records delivered after stop = %d, want 0", got)
	}
}
