package mcpserver

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestStartupOnlyConnectsEnabledTrustedOptIns(t *testing.T) {
	r := &batchRepository{values: map[string]Server{}}
	c := newBatchConnector()
	for _, id := range []string{"auto", "manual", "disabled", "untrusted", "offline"} {
		r.values[id] = Server{ID: id, Namespace: id, AutoStart: id != "manual", Enabled: id != "disabled", Trust: TrustUser, Status: StatusDisconnected}
	}
	u := r.values["untrusted"]
	u.Trust = TrustUntrusted
	r.values[u.ID] = u
	c.fail["offline"] = fmt.Errorf("fixture offline")
	s := NewService(r, c)
	result, err := s.ConnectOnStartup(context.Background())
	if err != nil || result.Succeeded != 1 || result.Failed != 1 || len(c.connectCalls) != 2 {
		t.Fatal(result, err, c.connectCalls)
	}
	if _, err := s.Disconnect(context.Background(), "auto"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Connected("auto") || c.connectCalls["auto"] != 1 {
		t.Fatal("discovery reconnected manually disconnected server")
	}
}

func TestStartupConnectCancellation(t *testing.T) {
	c := newBatchConnector()
	c.delay = time.Hour
	c.started = make(chan string, 1)
	s := NewService(&batchRepository{values: map[string]Server{"exa": {ID: "exa", Enabled: true, AutoStart: true, Trust: TrustUser}}}, c)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _, _ = s.ConnectOnStartup(ctx) }()
	select {
	case <-c.started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("startup did not cancel")
	}
}

func TestAutoStartRequiresTrustButSaveDoesNotConnect(t *testing.T) {
	c := newBatchConnector()
	s := NewService(&batchRepository{values: map[string]Server{}}, c)
	cmd := SaveCommand{Name: "Exa", Namespace: "exa", Transport: TransportStreamableHTTP, URL: "https://mcp.exa.ai/mcp?tools=web_search_exa", Enabled: true, AutoStart: true, Trust: TrustUntrusted}
	if _, err := s.Save(context.Background(), cmd); err == nil {
		t.Fatal("untrusted auto start accepted")
	}
	cmd.Trust = TrustUser
	if _, err := s.Save(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if len(c.connectCalls) != 0 {
		t.Fatal("Save connected")
	}
}
