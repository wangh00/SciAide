package modelutil

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
)

const (
	maxSnapshotBody   = 4 << 20
	maxSnapshotItems  = 128
	maxSnapshotScopes = 64
)

// DebugRequestSnapshot is a private structural inventory, NOT a prompt dump,
// tokenizer result, packet capture, or proof of server receipt. Fingerprints
// use a fresh process-instance HMAC key which is never persisted.
type DebugRequestSnapshot struct {
	Version             int                    `json:"version"`
	Session             string                 `json:"session"`
	Sequence            uint64                 `json:"sequence"`
	Protocol            modelcap.APIProtocol   `json:"protocol"`
	Stage               string                 `json:"stage"`
	StartedAt           time.Time              `json:"startedAt"`
	ObservedAt          time.Time              `json:"observedAt"`
	ElapsedMillis       int64                  `json:"elapsedMillis"`
	Method              string                 `json:"method"`
	Outcome             string                 `json:"outcome"`
	HTTPStatus          int                    `json:"httpStatus,omitempty"`
	RunID               string                 `json:"runId,omitempty"`
	RunKey              string                 `json:"runKey,omitempty"`
	TurnIndex           int                    `json:"turnIndex,omitempty"`
	Kind                string                 `json:"kind"`
	ConfiguredBudget    *DebugConfiguredBudget `json:"configuredBudget,omitempty"`
	DeclaredBodyBytes   int64                  `json:"declaredBodyBytes"`
	BodyBytes           int                    `json:"bodyBytes,omitempty"`
	Inspection          string                 `json:"inspection"`
	WireFingerprint     string                 `json:"wireFingerprint,omitempty"`
	SettingsFingerprint string                 `json:"settingsFingerprint,omitempty"`
	InstructionBytes    int                    `json:"instructionBytes"`
	InputCount          int                    `json:"inputCount"`
	InputBytes          int                    `json:"inputBytes"`
	ToolCount           int                    `json:"toolCount"`
	ToolBytes           int                    `json:"toolBytes"`
	Inputs              []DebugRequestItem     `json:"inputs,omitempty"`
	Tools               []DebugRequestItem     `json:"tools,omitempty"`
	OmittedInputs       int                    `json:"omittedInputs,omitempty"`
	OmittedTools        int                    `json:"omittedTools,omitempty"`
	Stream              *bool                  `json:"stream,omitempty"`
	ToolChoice          string                 `json:"toolChoice"`
	Reasoning           string                 `json:"reasoning"`
	MaxOutputTokens     *int64                 `json:"maxOutputTokens,omitempty"`
	Comparison          string                 `json:"comparison"`
	Delta               *DebugRequestDelta     `json:"delta,omitempty"`
}

type DebugConfiguredBudget struct {
	Source            string `json:"source"`
	WindowTokens      int    `json:"windowTokens"`
	EffectiveTokens   int    `json:"effectiveTokens"`
	AutoCompactTokens int    `json:"autoCompactTokens"`
	WindowSource      string `json:"windowSource"`
}

type DebugRequestItem struct {
	Index       int    `json:"index"`
	Kind        string `json:"kind"`
	Role        string `json:"role"`
	JSONBytes   int    `json:"jsonBytes"`
	SchemaBytes int    `json:"schemaBytes,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

type DebugRequestDelta struct {
	PreviousSequence  uint64 `json:"previousSequence"`
	BodyByteDelta     int    `json:"bodyByteDelta"`
	InputCountDelta   int    `json:"inputCountDelta"`
	ToolCountDelta    int    `json:"toolCountDelta"`
	CommonInputPrefix int    `json:"commonInputPrefix"`
	CommonToolPrefix  int    `json:"commonToolPrefix"`
	SameWire          bool   `json:"sameWire"`
	SettingsChanged   bool   `json:"settingsChanged"`
	CompleteInventory bool   `json:"completeInventory"`
}

type RequestSnapshotObserver struct {
	mu       sync.Mutex
	key      [32]byte
	session  string
	sequence uint64
	previous map[string]DebugRequestSnapshot
	emit     func(DebugRequestSnapshot)
}

// One observer is shared by an opted-in resolver instance. Nil callbacks do
// not enable inspection. Reopening a capture uses a NEW session/key/baseline.
func NewRequestSnapshotObserver(emit func(DebugRequestSnapshot)) (*RequestSnapshotObserver, error) {
	if emit == nil {
		return nil, nil
	}
	s := &RequestSnapshotObserver{previous: make(map[string]DebugRequestSnapshot), emit: emit}
	if _, err := rand.Read(s.key[:]); err != nil {
		return nil, err
	}
	s.session = s.fingerprint([]byte("snapshot-session"))[:32]
	return s, nil
}

func (s *RequestSnapshotObserver) fingerprint(data []byte) string {
	h := hmac.New(sha256.New, s.key[:])
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// Nil preserves the exact existing client/transport. The observer never
// consumes req.Body or response.Body and never gates or retries a request.
func WithRequestSnapshots(client *http.Client, protocol modelcap.APIProtocol, observer *RequestSnapshotObserver) *http.Client {
	if observer == nil {
		return client
	}
	copy := *client
	base := copy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copy.Transport = snapshotTransport{base: base, protocol: protocol, observer: observer}
	return &copy
}

type snapshotTransport struct {
	base     http.RoundTripper
	protocol modelcap.APIProtocol
	observer *RequestSnapshotObserver
}

func (t snapshotTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t snapshotTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now()
	response, err := t.base.RoundTrip(req)
	ended := time.Now()
	var admission *modelRequestGateError
	if !errors.As(err, &admission) {
		// Post-transport observation cannot insert disk IO between the durable
		// admission check and the original network dispatch.
		t.observer.record(req, t.protocol, response, err, started, ended)
	}
	return response, err
}

func (s *RequestSnapshotObserver) record(req *http.Request, protocol modelcap.APIProtocol, response *http.Response, requestErr error, started, ended time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	v := DebugRequestSnapshot{Version: 1, Session: s.session, Sequence: s.sequence, Protocol: protocol, Stage: "admitted_transport_return", StartedAt: started.UTC(), ObservedAt: ended.UTC(), ElapsedMillis: max(0, ended.Sub(started).Milliseconds()), Method: "other", Outcome: "transport_failure", DeclaredBodyBytes: req.ContentLength, Inspection: "body_unavailable", Kind: "unbound", Comparison: "unbound", ToolChoice: "absent", Reasoning: "absent"}
	if !protocol.Valid() {
		v.Protocol = "unknown"
	}
	if req.Method == http.MethodPost || req.Method == http.MethodGet {
		v.Method = req.Method
	}
	if response != nil && response.StatusCode >= 100 && response.StatusCode <= 599 {
		v.HTTPStatus = response.StatusCode
		v.Outcome = "response_headers"
	}
	if response == nil {
		terminal := requestErr
		for i := 0; i < 32 && errors.Unwrap(terminal) != nil; i++ {
			terminal = errors.Unwrap(terminal)
		}
		var timeout net.Error
		switch {
		case terminal == context.Canceled:
			v.Outcome = "context_canceled"
		case terminal == context.DeadlineExceeded:
			v.Outcome = "context_deadline"
		case req.Context().Err() != nil && errors.Is(requestErr, context.Cause(req.Context())):
			// net/http can return a WithCancelCause host cause instead of the
			// context sentinel. Require that exact cause in the returned chain;
			// a concurrently cancelled context alone must not relabel a fault.
			v.Outcome = "context_canceled"
			if req.Context().Err() == context.DeadlineExceeded {
				v.Outcome = "context_deadline"
			}
		case errors.As(requestErr, &timeout) && timeout.Timeout():
			v.Outcome = "transport_timeout"
		}
	}
	meta := model.RequestTraceContextFrom(req.Context())
	if meta.RunID != "" && len(meta.RunID) <= 128 && meta.TurnIndex > 0 && meta.TurnIndex <= 1_000_000 {
		v.RunKey = s.fingerprint([]byte("run:" + meta.RunID))
		if validSnapshotRunID(meta.RunID) {
			v.RunID = meta.RunID
		}
		v.TurnIndex = meta.TurnIndex
		v.Kind = "conversation"
		if meta.Kind == "compaction" {
			v.Kind = meta.Kind
		}
		if meta.WindowTokens > 0 && meta.WindowTokens <= 10_000_000 && meta.EffectiveTokens > 0 && meta.EffectiveTokens <= meta.WindowTokens && meta.AutoCompactTokens > 0 && meta.AutoCompactTokens <= meta.WindowTokens {
			source := "unknown"
			switch meta.WindowSource {
			case "manual", "provider", "fallback", "builtin", "inferred":
				source = meta.WindowSource
			}
			v.ConfiguredBudget = &DebugConfiguredBudget{Source: "host_run_configuration_not_token_measurement", WindowTokens: meta.WindowTokens, EffectiveTokens: meta.EffectiveTokens, AutoCompactTokens: meta.AutoCompactTokens, WindowSource: source}
		}
	}
	if !protocol.Valid() {
		v.Inspection = "unsupported_protocol"
	} else if req.Body == nil || req.Body == http.NoBody {
		v.Inspection = "no_body"
	} else if req.ContentLength > maxSnapshotBody {
		v.Inspection = "body_limit"
	} else if req.GetBody != nil {
		body, e := req.GetBody()
		if e == nil && body != nil {
			data, readErr := io.ReadAll(io.LimitReader(body, maxSnapshotBody+1))
			_ = body.Close()
			if readErr != nil {
				v.Inspection = "body_read_failed"
			} else if len(data) > maxSnapshotBody {
				v.Inspection = "body_limit"
			} else {
				s.inventory(data, &v)
			}
		}
	}
	s.compare(&v)
	// A broken opt-in sink must not replace the real HTTP result or trigger a retry.
	func() { defer func() { _ = recover() }(); s.emit(v) }()
}

func validSnapshotRunID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func snapshotPrefix(a, b []DebugRequestItem) int {
	n := 0
	for n < len(a) && n < len(b) && a[n].Fingerprint == b[n].Fingerprint {
		n++
	}
	return n
}

func (s *RequestSnapshotObserver) compare(v *DebugRequestSnapshot) {
	if v.RunKey == "" {
		return
	}
	if v.Inspection != "complete" {
		v.Comparison = "inspection_incomplete"
		return
	}
	key := string(v.Protocol) + ":" + v.RunKey + ":" + v.Kind
	v.Comparison = "no_previous_observation"
	if old, ok := s.previous[key]; ok {
		if v.TurnIndex < old.TurnIndex {
			v.Comparison = "out_of_order"
			return
		}
		v.Comparison = "previous_inspected_same_run_and_kind"
		v.Delta = &DebugRequestDelta{PreviousSequence: old.Sequence, BodyByteDelta: v.BodyBytes - old.BodyBytes, InputCountDelta: v.InputCount - old.InputCount, ToolCountDelta: v.ToolCount - old.ToolCount, CommonInputPrefix: snapshotPrefix(old.Inputs, v.Inputs), CommonToolPrefix: snapshotPrefix(old.Tools, v.Tools), SameWire: old.WireFingerprint == v.WireFingerprint, SettingsChanged: old.SettingsFingerprint != v.SettingsFingerprint, CompleteInventory: v.OmittedInputs == 0 && v.OmittedTools == 0 && old.OmittedInputs == 0 && old.OmittedTools == 0}
	}
	if _, exists := s.previous[key]; !exists && len(s.previous) >= maxSnapshotScopes {
		var oldest string
		seq := ^uint64(0)
		for k, p := range s.previous {
			if p.Sequence < seq {
				oldest, seq = k, p.Sequence
			}
		}
		delete(s.previous, oldest)
	}
	// Do not retain nested Delta chains or any request/response/body references.
	copy := *v
	copy.Delta = nil
	copy.ConfiguredBudget = nil
	copy.Stream = nil
	copy.Inputs = append([]DebugRequestItem(nil), v.Inputs...)
	copy.Tools = append([]DebugRequestItem(nil), v.Tools...)
	s.previous[key] = copy
}
