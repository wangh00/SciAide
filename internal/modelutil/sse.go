package modelutil

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

var errSSETooLarge = errors.New("SSE event exceeds size limit")

// SSEDecoder frames data fields at the event boundary, rather than treating
// each physical line as JSON. It retains the existing bounded payload limit.
type SSEDecoder struct {
	lines         *bufio.Scanner
	limit         int
	text          string
	eventType     string
	priorType     string
	channelStart  int
	eventComplete bool
	err           error
	progress      func()
	lastProgress  func() time.Time
}

func NewSSEDecoder(reader io.Reader, limit int) *SSEDecoder {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, min(64*1024, limit)), limit)
	return &SSEDecoder{lines: scanner, limit: limit}
}

func (s *SSEDecoder) Scan() bool {
	s.text = ""
	s.eventType = ""
	s.priorType = ""
	s.channelStart = 0
	s.eventComplete = false
	if s.err != nil {
		return false
	}
	var data strings.Builder
	hasData := false
	lastDataType := ""
	for s.lines.Scan() {
		line := s.lines.Text()
		if line == "" {
			if hasData {
				s.text = data.String()
				s.eventComplete = true
				return true
			}
			s.eventType = "" // an event-only block must not label the next event
			s.priorType = ""
			s.channelStart = 0
			lastDataType = ""
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ") // SSE removes one optional space
		if field == "event" {
			if hasData {
				s.priorType = lastDataType      // an intervening header has no payload
				s.channelStart = data.Len() + 1 // next data field inserts one LF
			}
			s.eventType = value // the final event field owns this dispatch
			continue
		}
		if field != "data" {
			continue // comments, id and retry are not JSON data
		}
		extra := len(value)
		if hasData {
			extra++
		}
		if extra > s.limit-data.Len() {
			s.err = errSSETooLarge
			return false
		}
		if hasData {
			data.WriteByte('\n')
		}
		data.WriteString(value)
		hasData = true
		lastDataType = s.eventType
	}
	if err := s.lines.Err(); err != nil {
		s.err = err
		return false
	}
	// Preserve gateways that omit the final blank separator. A complete JSON
	// event is still checked by the protocol; half JSON is never repaired here.
	if hasData {
		s.text = data.String()
		return true
	}
	return false
}

func (s *SSEDecoder) Text() string { return s.text }
func (s *SSEDecoder) Err() error   { return s.err }

// EventType retains the explicit SSE channel, without interpreting it as data.
// An absent or explicitly empty event field returns the empty string.
func (s *SSEDecoder) EventType() string { return s.eventType }

// CoalescedErrorFields retains evidence of a channel header inside a dispatch;
// it does NOT change standard SSE framing or return a successful split event.
// Protocol callers may validate the two independent fields only as a failure.
func (s *SSEDecoder) CoalescedErrorFields() (prefix, priorType, terminal string, ok bool) {
	if !s.eventComplete || s.eventType != "error" || s.priorType == "" || s.channelStart <= 0 || s.channelStart >= len(s.text) {
		return "", "", "", false
	}
	return s.text[:s.channelStart-1], s.priorType, s.text[s.channelStart:], true
}

// EventComplete distinguishes an explicit SSE separator from EOF while an
// event was still arriving. Only the latter may represent transport truncation.
func (s *SSEDecoder) EventComplete() bool { return s.eventComplete }

// MarkProgress is called only after a protocol parser observes an actual
// content/reasoning/tool argument increment or a completed native content item.
func (s *SSEDecoder) MarkProgress() {
	if s.progress != nil {
		s.progress()
	}
}

func (s *SSEDecoder) LastProgressAt() time.Time {
	if s.lastProgress != nil {
		return s.lastProgress()
	}
	return time.Time{}
}

func ClassifySSEJSONError(source, message string, data []byte, err error, eventComplete bool) error {
	metadata := streamEventMetadata(data)
	metadata["jsonError"], metadata["jsonByteOffset"] = providerJSONLocation(err)
	metadata["eventBoundary"] = "transport_eof"
	if eventComplete {
		metadata["eventBoundary"] = "complete"
	}
	details := ProviderErrorDetails(source, 0, encodedProviderMetadata(metadata))
	var syntax *json.SyntaxError
	if !eventComplete && errors.As(err, &syntax) && syntax.Offset >= int64(len(data)) && syntax.Error() == "unexpected end of JSON input" {
		// An interrupted prefix is retryable; invalid syntax or protocol shape
		// remains permanent. Never append guessed bytes or accept a partial draft.
		return ErrorWithDetails("MODEL_STREAM_INTERRUPTED", "模型流事件在 JSON 完成前中断，SciAide 将等待后重连。", details, true, ProviderJSONErrorCause(err))
	}
	return ErrorWithDetails("MODEL_STREAM_INVALID", message+"未接受残缺结果，不会自动重复本次请求。", details, false, ProviderJSONErrorCause(err))
}

func ClassifySSEReadError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, errStreamStalled) {
		return ErrorWithDetails("MODEL_STREAM_STALLED", "模型流长时间没有有效进展，正在尝试重连。", err.Error(), true, err)
	}
	if errors.Is(err, errSSETooLarge) || errors.Is(err, bufio.ErrTooLong) {
		return Error("MODEL_STREAM_INVALID", "模型流事件超出大小限制，已停止处理。", false, err)
	}
	return Error("MODEL_UNAVAILABLE", "模型连接意外中断，SciAide 将自动重连。", true, err)
}
