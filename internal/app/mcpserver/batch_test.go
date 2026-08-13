package mcpserver

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type batchRepository struct {
	mu     sync.Mutex
	values map[string]Server
}

func (r *batchRepository) Save(_ context.Context, value Server) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[value.ID] = value
	return nil
}
func (r *batchRepository) Get(_ context.Context, id string) (Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[id]
	if !ok {
		return Server{}, fmt.Errorf("MCP server not found")
	}
	return value, nil
}
func (r *batchRepository) List(context.Context) ([]Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make([]Server, 0, len(r.values))
	for _, value := range r.values {
		values = append(values, value)
	}
	return values, nil
}
func (r *batchRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, id)
	return nil
}
func (r *batchRepository) UpdateRuntime(_ context.Context, id string, status Status, protocolVersion, serverVersion string, toolCount, resourceCount, promptCount int, lastError string, connectedAt *time.Time, updatedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := r.values[id]
	value.Status, value.ProtocolVersion, value.ServerVersion = status, protocolVersion, serverVersion
	value.ToolCount, value.ResourceCount, value.PromptCount = toolCount, resourceCount, promptCount
	value.LastError, value.LastConnectedAt, value.UpdatedAt = lastError, connectedAt, updatedAt
	r.values[id] = value
	return nil
}

type batchConnector struct {
	mu            sync.Mutex
	connected     map[string]bool
	fail          map[string]error
	delay         time.Duration
	active        int
	maxActive     int
	connectCalls  map[string]int
	disconnectIDs []string
	started       chan string
	pending       map[string]chan struct{}
}

func newBatchConnector() *batchConnector {
	return &batchConnector{connected: map[string]bool{}, fail: map[string]error{}, connectCalls: map[string]int{}, pending: map[string]chan struct{}{}}
}

func (c *batchConnector) Connect(ctx context.Context, server Server) (CapabilitySnapshot, error) {
	c.mu.Lock()
	c.active++
	if c.active > c.maxActive {
		c.maxActive = c.active
	}
	c.connectCalls[server.ID]++
	delay, failure := c.delay, c.fail[server.ID]
	cancelled := make(chan struct{})
	c.pending[server.ID] = cancelled
	c.mu.Unlock()
	if c.started != nil {
		select {
		case c.started <- server.ID:
		default:
		}
	}
	defer func() {
		c.mu.Lock()
		c.active--
		delete(c.pending, server.ID)
		c.mu.Unlock()
	}()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return CapabilitySnapshot{}, ctx.Err()
		case <-cancelled:
			return CapabilitySnapshot{}, context.Canceled
		}
	}
	if failure != nil {
		return CapabilitySnapshot{}, failure
	}
	c.mu.Lock()
	c.connected[server.ID] = true
	c.mu.Unlock()
	return CapabilitySnapshot{Tools: []ToolInfo{{QualifiedName: "mcp." + server.Namespace + ".tool"}}}, nil
}
func (c *batchConnector) Disconnect(_ context.Context, serverID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.connected, serverID)
	if pending := c.pending[serverID]; pending != nil {
		close(pending)
		delete(c.pending, serverID)
	}
	c.disconnectIDs = append(c.disconnectIDs, serverID)
	return nil
}
func (c *batchConnector) Connected(serverID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected[serverID] || c.pending[serverID] != nil
}

func batchFixture(ids ...string) (*Service, *batchRepository, *batchConnector) {
	repository := &batchRepository{values: map[string]Server{}}
	for _, id := range ids {
		repository.values[id] = Server{ID: id, Name: "Server " + id, Namespace: id, Enabled: true, Trust: TrustUser, Status: StatusDisconnected, TimeoutSeconds: 30}
	}
	connector := newBatchConnector()
	return NewService(repository, connector), repository, connector
}

func TestConnectBatchIsBoundedOrderedAndIsolatesFailures(t *testing.T) {
	service, _, connector := batchFixture("one", "two", "three", "four", "five", "six")
	connector.delay = 10 * time.Millisecond
	connector.fail["two"] = fmt.Errorf("dial failed")

	result, err := service.ConnectBatch(context.Background(), []string{"one", "two", "three", "four", "five", "six"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Succeeded != 5 || result.Failed != 1 || result.Skipped != 0 {
		t.Fatalf("unexpected summary: %#v", result)
	}
	for index, id := range []string{"one", "two", "three", "four", "five", "six"} {
		if result.Items[index].ServerID != id {
			t.Fatalf("result order changed: %#v", result.Items)
		}
	}
	if result.Items[1].Status != BatchStatusFailed || result.Items[2].Status != BatchStatusSucceeded {
		t.Fatalf("failure was not isolated: %#v", result.Items)
	}
	connector.mu.Lock()
	maxActive := connector.maxActive
	connector.mu.Unlock()
	if maxActive > batchWorkerLimit || maxActive < 2 {
		t.Fatalf("maximum concurrency = %d", maxActive)
	}
}

func TestConnectBatchSkipsIneligibleAndDeduplicatesIDs(t *testing.T) {
	service, repository, connector := batchFixture("ready", "disabled", "untrusted", "valid")
	repository.values["ready"] = Server{ID: "ready", Name: "Ready", Namespace: "ready", Enabled: true, Trust: TrustUser, Status: StatusReady, TimeoutSeconds: 30}
	connector.connected["ready"] = true
	repository.values["disabled"] = Server{ID: "disabled", Name: "Disabled", Namespace: "disabled", Enabled: false, Trust: TrustUser, Status: StatusDisabled, TimeoutSeconds: 30}
	repository.values["untrusted"] = Server{ID: "untrusted", Name: "Untrusted", Namespace: "untrusted", Enabled: true, Trust: TrustUntrusted, Status: StatusDisconnected, TimeoutSeconds: 30}

	result, err := service.ConnectBatch(context.Background(), []string{"ready", "disabled", "untrusted", "valid", "valid"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 4 || result.Succeeded != 1 || result.Skipped != 3 || result.Failed != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	connector.mu.Lock()
	calls := connector.connectCalls["valid"]
	connector.mu.Unlock()
	if calls != 1 {
		t.Fatalf("duplicate server connected %d times", calls)
	}
}

func TestDisconnectBatchClosesOnlyActiveServers(t *testing.T) {
	service, repository, connector := batchFixture("active", "idle")
	repository.values["active"] = Server{ID: "active", Name: "Active", Namespace: "active", Enabled: true, Trust: TrustUser, Status: StatusReady}
	connector.connected["active"] = true

	result, err := service.DisconnectBatch(context.Background(), []string{"idle", "active"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Succeeded != 1 || result.Skipped != 1 || result.Items[0].Status != BatchStatusSkipped || result.Items[1].Status != BatchStatusSucceeded {
		t.Fatalf("unexpected result: %#v", result)
	}
	connector.mu.Lock()
	disconnected := append([]string(nil), connector.disconnectIDs...)
	connector.mu.Unlock()
	if len(disconnected) != 1 || disconnected[0] != "active" {
		t.Fatalf("disconnect calls = %#v", disconnected)
	}
}

func TestConnectBatchHonorsCancellationAndInputLimit(t *testing.T) {
	ids := []string{"one", "two", "three", "four", "five", "six"}
	service, _, connector := batchFixture(ids...)
	connector.delay = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := service.ConnectBatch(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != len(ids) {
		t.Fatalf("cancelled result = %#v", result)
	}

	tooMany := make([]string, maxBatchServers+1)
	if _, err := service.ConnectBatch(context.Background(), tooMany); err == nil {
		t.Fatal("oversized batch was accepted")
	}
}

func TestDisconnectBatchCancelsPendingConnect(t *testing.T) {
	service, _, connector := batchFixture("pending")
	connector.delay = time.Second
	connector.started = make(chan string, 1)
	connectDone := make(chan BatchResult, 1)
	go func() {
		result, _ := service.ConnectBatch(context.Background(), []string{"pending"})
		connectDone <- result
	}()
	select {
	case <-connector.started:
	case <-time.After(time.Second):
		t.Fatal("connect did not start")
	}

	disconnectResult, err := service.DisconnectBatch(context.Background(), []string{"pending"})
	if err != nil || disconnectResult.Succeeded != 1 {
		t.Fatalf("disconnect pending = %#v, %v", disconnectResult, err)
	}
	select {
	case connectResult := <-connectDone:
		if connectResult.Failed != 1 {
			t.Fatalf("cancelled connect result = %#v", connectResult)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled connect did not return")
	}
	value, err := service.Get(context.Background(), "pending")
	if err != nil || value.Status != StatusDisconnected || value.LastError != "" {
		t.Fatalf("pending runtime state = %#v, %v", value, err)
	}
}
