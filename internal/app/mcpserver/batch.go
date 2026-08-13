package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const (
	maxBatchServers      = 128
	batchWorkerLimit     = 3
	BatchStatusSucceeded = "succeeded"
	BatchStatusSkipped   = "skipped"
	BatchStatusFailed    = "failed"
)

// BatchCommand is shared by the bulk connect and disconnect endpoints. IDs
// are deliberately explicit: even "connect all" is resolved by the UI and
// then revalidated by the service, so a batch can never bypass trust checks.
type BatchCommand struct {
	ServerIDs []string `json:"serverIds"`
}

type BatchItemResult struct {
	ServerID string `json:"serverId"`
	Name     string `json:"name,omitempty"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
	Server   Server `json:"server"`
}

type BatchResult struct {
	Succeeded int               `json:"succeeded"`
	Skipped   int               `json:"skipped"`
	Failed    int               `json:"failed"`
	Items     []BatchItemResult `json:"items"`
}

// ConnectBatch connects trusted and enabled servers with a small worker pool.
// A failure is isolated to its server and results retain the caller's order.
func (s *Service) ConnectBatch(ctx context.Context, serverIDs []string) (BatchResult, error) {
	return s.runBatch(ctx, serverIDs, s.connectBatchItem)
}

// DisconnectBatch cancels pending connections as well as closing established
// sessions. Already inactive servers are reported as skipped.
func (s *Service) DisconnectBatch(ctx context.Context, serverIDs []string) (BatchResult, error) {
	return s.runBatch(ctx, serverIDs, s.disconnectBatchItem)
}

type batchOperation func(context.Context, string) BatchItemResult

func (s *Service) runBatch(ctx context.Context, serverIDs []string, operation batchOperation) (BatchResult, error) {
	ids, err := normalizeBatchIDs(serverIDs)
	if err != nil {
		return BatchResult{}, err
	}
	result := BatchResult{Items: make([]BatchItemResult, len(ids))}
	if len(ids) == 0 {
		return result, nil
	}
	if ctx.Err() != nil {
		for index, id := range ids {
			result.Items[index] = BatchItemResult{ServerID: id, Status: BatchStatusSkipped, Message: "operation cancelled before this server was processed"}
		}
		result.summarize()
		return result, nil
	}

	jobs := make(chan int)
	var workers sync.WaitGroup
	workerCount := batchWorkerLimit
	if len(ids) < workerCount {
		workerCount = len(ids)
	}
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				result.Items[index] = operation(ctx, ids[index])
			}
		}()
	}

	next := 0
dispatch:
	for ; next < len(ids); next++ {
		if ctx.Err() != nil {
			break dispatch
		}
		select {
		case jobs <- next:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	workers.Wait()
	for ; next < len(ids); next++ {
		result.Items[next] = BatchItemResult{
			ServerID: ids[next],
			Status:   BatchStatusSkipped,
			Message:  "operation cancelled before this server was processed",
		}
	}
	result.summarize()
	return result, nil
}

func normalizeBatchIDs(serverIDs []string) ([]string, error) {
	if len(serverIDs) > maxBatchServers {
		return nil, fmt.Errorf("MCP batch cannot contain more than %d server IDs", maxBatchServers)
	}
	seen := make(map[string]struct{}, len(serverIDs))
	ids := make([]string, 0, len(serverIDs))
	for _, raw := range serverIDs {
		value := strings.TrimSpace(raw)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	return ids, nil
}

func (s *Service) connectBatchItem(ctx context.Context, serverID string) BatchItemResult {
	item := BatchItemResult{ServerID: serverID}
	if serverID == "" {
		item.Status, item.Message = BatchStatusFailed, "MCP server id is required"
		return item
	}
	value, err := s.Get(ctx, serverID)
	if err != nil {
		item.Status, item.Message = BatchStatusFailed, publicError(err)
		return item
	}
	item.Name, item.Server = value.Name, value
	if !value.Enabled {
		item.Status, item.Message = BatchStatusSkipped, "server is disabled"
		return item
	}
	if value.Trust != TrustUser {
		item.Status, item.Message = BatchStatusSkipped, "server is not trusted"
		return item
	}
	if (s.connector != nil && s.connector.Connected(value.ID)) || batchActiveStatus(value.Status) {
		item.Status, item.Message = BatchStatusSkipped, "server is already connected or connecting"
		return item
	}
	connected, err := s.Connect(ctx, value.ID)
	if err != nil {
		item.Status, item.Message = BatchStatusFailed, publicError(err)
		if latest, getErr := s.Get(context.Background(), value.ID); getErr == nil {
			item.Server = latest
		}
		return item
	}
	item.Status, item.Server = BatchStatusSucceeded, connected
	return item
}

func (s *Service) disconnectBatchItem(ctx context.Context, serverID string) BatchItemResult {
	item := BatchItemResult{ServerID: serverID}
	if serverID == "" {
		item.Status, item.Message = BatchStatusFailed, "MCP server id is required"
		return item
	}
	value, err := s.Get(ctx, serverID)
	if err != nil {
		item.Status, item.Message = BatchStatusFailed, publicError(err)
		return item
	}
	item.Name, item.Server = value.Name, value
	connected := s.connector != nil && s.connector.Connected(value.ID)
	if !connected && !batchActiveStatus(value.Status) {
		item.Status, item.Message = BatchStatusSkipped, "server is already disconnected"
		return item
	}
	disconnected, err := s.Disconnect(ctx, value.ID)
	if err != nil {
		item.Status, item.Message = BatchStatusFailed, publicError(err)
		if latest, getErr := s.Get(context.Background(), value.ID); getErr == nil {
			item.Server = latest
		}
		return item
	}
	item.Status, item.Server = BatchStatusSucceeded, disconnected
	return item
}

func batchActiveStatus(status Status) bool {
	switch status {
	case StatusStarting, StatusInitializing, StatusReady, StatusDegraded, StatusStopping:
		return true
	default:
		return false
	}
}

func (r *BatchResult) summarize() {
	for _, item := range r.Items {
		switch item.Status {
		case BatchStatusSucceeded:
			r.Succeeded++
		case BatchStatusSkipped:
			r.Skipped++
		default:
			r.Failed++
		}
	}
}
