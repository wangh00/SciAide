package citation

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const (
	KindKnowledgeChunk = "knowledge_chunk"
	KnowledgeToolName  = "builtin.knowledge.search"
)

func KnowledgeReference(runID, indexVersionID, chunkID, quoteSHA256 string) string {
	parts := []string{
		strings.TrimSpace(runID),
		strings.TrimSpace(indexVersionID),
		strings.TrimSpace(chunkID),
		strings.TrimSpace(quoteSHA256),
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "[K-" + strings.ToUpper(hex.EncodeToString(digest[:6])) + "]"
}

func QuoteSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func Resolve(runID, messageID, text string, calls []tool.Call, at time.Time) []conversation.Citation {
	runID, messageID = strings.TrimSpace(runID), strings.TrimSpace(messageID)
	if runID == "" || messageID == "" || text == "" {
		return []conversation.Citation{}
	}
	type candidate struct {
		callID string
		ref    tool.CitationRef
		hash   string
	}
	candidates := make(map[string]candidate)
	ambiguous := make(map[string]struct{})
	for _, call := range calls {
		if call.ID == "" || call.RunID != runID || call.ToolName != KnowledgeToolName || call.Status != tool.CallCompleted || call.Result == nil || call.Result.Status != tool.ResultSuccess {
			continue
		}
		for _, ref := range call.Result.Citations {
			if !validKnowledgeRef(runID, ref) {
				continue
			}
			fingerprint := evidenceFingerprint(ref)
			if previous, exists := candidates[ref.Reference]; exists && previous.hash != fingerprint {
				delete(candidates, ref.Reference)
				ambiguous[ref.Reference] = struct{}{}
				continue
			}
			if _, collision := ambiguous[ref.Reference]; collision {
				continue
			}
			candidates[ref.Reference] = candidate{callID: call.ID, ref: ref, hash: fingerprint}
		}
	}
	markers := extractMarkers(text)
	result := make([]conversation.Citation, 0, len(markers))
	seen := make(map[string]struct{}, len(markers))
	for _, marker := range markers {
		if _, exists := seen[marker]; exists {
			continue
		}
		value, exists := candidates[marker]
		if !exists {
			continue
		}
		seen[marker] = struct{}{}
		ref := value.ref
		result = append(result, conversation.Citation{
			ID: deterministicID(runID, messageID, marker), MessageID: messageID, RunID: runID, ToolCallID: value.callID,
			ProjectID: ref.ProjectID, Reference: marker, Ordinal: len(result), IndexVersionID: ref.IndexVersionID,
			DocumentID: ref.DocumentID, AttachmentID: ref.AttachmentID, ChunkID: ref.ChunkID,
			SourceName: ref.SourceName, MIMEType: ref.MIMEType, Locator: ref.Locator, Title: ref.Title,
			Quote: ref.Quote, QuoteSHA256: ref.QuoteSHA256, SourceStart: ref.SourceStart, SourceEnd: ref.SourceEnd, CreatedAt: at,
		})
	}
	return result
}

func validKnowledgeRef(runID string, ref tool.CitationRef) bool {
	if ref.ProjectID == "" || ref.IndexVersionID == "" || ref.DocumentID == "" || ref.AttachmentID == "" || ref.ChunkID == "" || ref.SourceName == "" || ref.Locator == "" || ref.Quote == "" {
		return false
	}
	quoteSHA256 := QuoteSHA256(ref.Quote)
	if ref.Kind != KindKnowledgeChunk || ref.QuoteSHA256 != quoteSHA256 || ref.Reference != KnowledgeReference(runID, ref.IndexVersionID, ref.ChunkID, quoteSHA256) {
		return false
	}
	if ref.SourceStart < 0 || ref.SourceEnd < ref.SourceStart {
		return false
	}
	return true
}

func evidenceFingerprint(ref tool.CitationRef) string {
	parts := []string{ref.Kind, ref.Reference, ref.ProjectID, ref.IndexVersionID, ref.DocumentID, ref.AttachmentID, ref.ChunkID, ref.SourceName, ref.MIMEType, ref.Locator, ref.Title, ref.QuoteSHA256, strconv.Itoa(ref.SourceStart), strconv.Itoa(ref.SourceEnd)}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}

func deterministicID(runID, messageID, reference string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + messageID + "\x00" + reference))
	return "citation_" + hex.EncodeToString(digest[:16])
}

func extractMarkers(text string) []string {
	runes := []rune(text)
	result := make([]string, 0)
	for index := 0; index+16 <= len(runes); index++ {
		if runes[index] != '[' || runes[index+1] != 'K' || runes[index+2] != '-' || runes[index+15] != ']' {
			continue
		}
		valid := true
		for offset := 3; offset < 15; offset++ {
			value := runes[index+offset]
			if !((value >= '0' && value <= '9') || (value >= 'A' && value <= 'F')) {
				valid = false
				break
			}
		}
		if valid {
			result = append(result, string(runes[index:index+16]))
			index += 15
		}
	}
	return result
}
