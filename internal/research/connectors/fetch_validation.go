package connectors

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	research "github.com/wangh00/SciAide/internal/app/research"
)

func usableSearchRecord(source, id, title string) bool {
	return research.IsUsableSourceRecord(source, research.Work{SourceRecordID: id, Title: title})
}

func validateSearchRecords(source string, rawCount int, values []research.Work, limit int) error {
	if rawCount == 0 {
		return nil
	}
	if limit > 0 && len(values) > limit {
		values = values[:limit]
	}
	for _, value := range values {
		if research.IsUsableSourceRecord(source, value) {
			return nil
		}
	}
	return &research.SourceError{SourceID: source, Code: research.FailureInvalidData, Message: "source returned records but no usable record in the requested window"}
}

func validateDeclaredJSONError(source string, raw json.RawMessage) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return &research.SourceError{SourceID: source, Code: research.FailureInvalidData, Message: "source declared an error in its success response"}
}

func validateCrossrefStatus(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	} // Existing minimum envelopes may omit status.
	var status string
	if err := json.Unmarshal(raw, &status); err != nil || status != "ok" {
		return &research.SourceError{SourceID: "crossref", Code: research.FailureInvalidData, Message: "Crossref response did not declare successful status"}
	}
	return nil
}

func validateFetchedRecords(source, requestedID string, values []research.Work) error {
	if len(values) != 1 {
		return &research.SourceError{SourceID: source, Code: research.FailureInvalidData, Message: "source did not return exactly one usable fetched record"}
	}
	return research.ValidateFetchedRecord(source, requestedID, values[0])
}

// Decode one entire XML document, not just the first root accepted by Unmarshal.
// Source schemas remain the caller's responsibility. Allow the usual prolog,
// comments and processing instructions, but not another root or outside text.
func decodeSourceXML(body []byte, destination any) error {
	// A UTF-8 BOM is valid only at the very start, not general XML whitespace.
	body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoded := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if !decoded {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.StartElement:
			if decoded {
				return fmt.Errorf("source XML contains multiple roots")
			}
			if err := decoder.DecodeElement(destination, &value); err != nil {
				return err
			}
			decoded = true
		case xml.CharData:
			if len(bytes.Trim(value, " \t\r\n")) != 0 {
				return fmt.Errorf("source XML contains text outside its root")
			}
		case xml.Comment:
		case xml.ProcInst:
			if decoded && strings.EqualFold(value.Target, "xml") {
				return fmt.Errorf("source XML contains a trailing declaration")
			}
		case xml.Directive:
			if decoded {
				return fmt.Errorf("source XML contains a trailing directive")
			}
		default:
			return fmt.Errorf("source XML contains an unexpected outside token")
		}
	}
}
