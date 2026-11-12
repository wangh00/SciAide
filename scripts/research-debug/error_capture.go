package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/model/responses"
)

type errorCapture struct {
	mu    sync.Mutex
	f     *os.File
	bytes int64
}

func openErrorCapture(root string) (*errorCapture, error) {
	return openCaptureFile(root, "provider-error-capture.jsonl")
}

func openStreamFaultCapture(root string) (*errorCapture, error) {
	return openCaptureFile(root, "stream-fault-capture.jsonl")
}

func openHTTPResponseCapture(root string) (*errorCapture, error) {
	return openCaptureFile(root, "http-response-capture.jsonl")
}

func openCaptureFile(root, name string) (*errorCapture, error) {
	f, err := os.OpenFile(filepath.Join(root, name), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &errorCapture{f: f, bytes: info.Size()}, nil
}

func (c *errorCapture) record(event responses.DebugProviderError) {
	c.recordJSON(struct {
		At       time.Time `json:"at"`
		Protocol string    `json:"protocol"`
		responses.DebugProviderError
	}{time.Now().UTC(), "openai_responses", event})
}

func (c *errorCapture) recordStreamFault(event responses.DebugStreamFault) {
	c.recordJSON(struct {
		At       time.Time `json:"at"`
		Protocol string    `json:"protocol"`
		responses.DebugStreamFault
	}{time.Now().UTC(), "openai_responses", event})
}

func (c *errorCapture) recordHTTPResponse(event responses.DebugHTTPResponse) {
	c.recordJSON(struct {
		At       time.Time `json:"at"`
		Protocol string    `json:"protocol"`
		responses.DebugHTTPResponse
	}{time.Now().UTC(), "openai_responses", event})
}

func (c *errorCapture) recordJSON(event any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := json.Marshal(event)
	if err != nil || c.bytes+int64(len(data))+1 > 1<<20 {
		return
	}
	n, _ := c.f.Write(append(data, '\n'))
	c.bytes += int64(n)
	_ = c.f.Sync()
}

func (c *errorCapture) Close() error { c.mu.Lock(); defer c.mu.Unlock(); return c.f.Close() }
