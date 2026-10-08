package modelutil

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

var errSSETooLarge = errors.New("SSE event exceeds size limit")

// SSEDecoder frames data fields at the event boundary, rather than treating
// each physical line as JSON. It retains the existing bounded payload limit.
type SSEDecoder struct {
	lines *bufio.Scanner
	limit int
	text  string
	err   error
}

func NewSSEDecoder(reader io.Reader, limit int) *SSEDecoder {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, min(64*1024, limit)), limit)
	return &SSEDecoder{lines: scanner, limit: limit}
}

func (s *SSEDecoder) Scan() bool {
	s.text = ""
	if s.err != nil {
		return false
	}
	var data strings.Builder
	hasData := false
	for s.lines.Scan() {
		line := s.lines.Text()
		if line == "" {
			if hasData {
				s.text = data.String()
				return true
			}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		if field != "data" {
			continue // comments, event, id and retry are not JSON data
		}
		value = strings.TrimPrefix(value, " ") // SSE removes one optional space
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

func ClassifySSEJSONError(source, message string, data []byte, err error) error {
	details := ProviderErrorDetails(source, 0, data)
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) && syntax.Offset >= int64(len(data)) && syntax.Error() == "unexpected end of JSON input" {
		// An interrupted prefix is retryable; invalid syntax or protocol shape
		// remains permanent. Never append guessed bytes or accept a partial draft.
		return ErrorWithDetails("MODEL_STREAM_INTERRUPTED", "模型流事件在 JSON 完成前中断，SciAide 将等待后重连。", details, true, err)
	}
	return ErrorWithDetails("MODEL_STREAM_INVALID", message, details, false, err)
}

func ClassifySSEReadError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, errSSETooLarge) || errors.Is(err, bufio.ErrTooLong) {
		return Error("MODEL_STREAM_INVALID", "模型流事件超出大小限制，已停止处理。", false, err)
	}
	return Error("MODEL_UNAVAILABLE", "模型连接意外中断，SciAide 将自动重连。", true, err)
}
