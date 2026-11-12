package responses

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

const streamFaultCaptureBytes = 32 * 1024
const streamFaultWireWindow = 128 * 1024

// DebugStreamFault is opt-in private diagnostic evidence, never a public model
// event or accepted output. Base64 preserves invalid UTF-8 and wire delimiters.
// Known configured secrets are redacted before capture truncation; this is not
// a guarantee against every transformation or partial echo of a secret.
type DebugStreamFault struct {
	EventComplete    bool   `json:"eventComplete"`
	JSONValid        bool   `json:"jsonValid"`
	DataBase64       string `json:"dataBase64"`
	DataBytes        int    `json:"dataBytes"`
	DataSHA256       string `json:"dataSha256"`
	DataTruncated    bool   `json:"dataTruncated"`
	DataRedacted     bool   `json:"dataRedacted"`
	WireTailBase64   string `json:"wireTailBase64"`
	WireBytesRead    int64  `json:"wireBytesRead"`
	WireTailSHA256   string `json:"wireTailSha256"`
	WireTailPartial  bool   `json:"wireTailPartial"`
	WireTailRedacted bool   `json:"wireTailRedacted"`
}

// Configure once before sharing a client. Desktop callers leave this nil.
func (c *Client) SetDebugStreamFaultObserver(observe func(DebugStreamFault)) {
	c.debugStreamObserver = observe
}

type streamWireTrace struct {
	io.ReadCloser
	mu   sync.Mutex
	tail []byte
	read int64
}

func (r *streamWireTrace) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.mu.Lock()
		r.read += int64(n)
		if n >= streamFaultWireWindow {
			r.tail = append(r.tail[:0], p[n-streamFaultWireWindow:n]...)
		} else {
			if excess := len(r.tail) + n - streamFaultWireWindow; excess > 0 {
				copy(r.tail, r.tail[excess:])
				r.tail = r.tail[:len(r.tail)-excess]
			}
			r.tail = append(r.tail, p[:n]...)
		}
		r.mu.Unlock()
	}
	return n, err
}

func (r *streamWireTrace) snapshot() ([]byte, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.tail...), r.read
}

func debugStreamFaultObserver(observe func(DebugStreamFault), secret []byte, headers map[string]string) func([]byte, bool, *streamWireTrace) {
	if observe == nil {
		return nil
	}
	redact := debugTextRedactor(secret, headers)
	return func(data []byte, complete bool, trace *streamWireTrace) {
		wire, read := trace.snapshot()
		cleanData, cleanWire := []byte(redact(string(data), 0)), []byte(redact(string(wire), 0))
		dataRedacted, wireRedacted := string(cleanData) != string(data), string(cleanWire) != string(wire)
		dataTruncated := len(cleanData) > streamFaultCaptureBytes
		if dataTruncated {
			cleanData = cleanData[:streamFaultCaptureBytes]
		}
		if len(wire) > streamFaultCaptureBytes {
			wire = wire[len(wire)-streamFaultCaptureBytes:]
		}
		if len(cleanWire) > streamFaultCaptureBytes {
			cleanWire = cleanWire[len(cleanWire)-streamFaultCaptureBytes:]
		}
		observe(DebugStreamFault{
			EventComplete: complete, JSONValid: json.Valid(data),
			DataBase64: base64.StdEncoding.EncodeToString(cleanData), DataBytes: len(data), DataSHA256: fmt.Sprintf("%x", sha256.Sum256(data)), DataTruncated: dataTruncated, DataRedacted: dataRedacted,
			WireTailBase64: base64.StdEncoding.EncodeToString(cleanWire), WireBytesRead: read, WireTailSHA256: fmt.Sprintf("%x", sha256.Sum256(wire)), WireTailPartial: read > int64(len(wire)) || int64(len(cleanWire)) < read, WireTailRedacted: wireRedacted,
		})
	}
}
