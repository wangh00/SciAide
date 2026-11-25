package contextmemory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
)

const ToolLedgerReadName = "builtin.context.tool_history"
const ToolLedgerRecentCalls = 8
const ToolLedgerIndexLimit = 4
const ToolLedgerPageChars = 1000

var ErrToolLedgerSnapshotUnavailable = errors.New("original tool model snapshot is missing or unsupported; it was not reconstructed")

// A reference seals an ordered prefix of existing tool records, not a new
// database or authorization token. New calls after the boundary do not change it.
type ToolLedgerRef struct {
	Version       int    `json:"version"`
	ThroughCallID string `json:"throughCallId"`
	SHA256        string `json:"ledgerSha256"`
	Count         int    `json:"count"`
}

type ToolLedgerEntry struct {
	CallID             string          `json:"callId"`
	Tool               string          `json:"tool"`
	Status             tool.CallStatus `json:"status"`
	ArgumentsChars     int             `json:"argumentsChars"`
	ArgumentsSHA256    string          `json:"argumentsSha256"`
	ModelContextChars  int             `json:"modelContextChars"`
	ModelContextSHA256 string          `json:"modelContextSha256"`
}

type ToolLedger struct {
	Ref     ToolLedgerRef
	Entries []ToolLedgerEntry
	Calls   []tool.Call `json:"-"`
}

func ToolLedgerHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Only same-run, terminal ordinary calls with an existing supported immutable
// model snapshot may leave the live window. Never reconstruct it from Result:
// Result can contain fields deliberately absent from the model projection.
func BuildToolLedger(calls []tool.Call, runID, throughID string) (ToolLedger, error) {
	ledger := ToolLedger{Ref: ToolLedgerRef{Version: 1, ThroughCallID: throughID}}
	if runID == "" || throughID == "" {
		return ledger, fmt.Errorf("tool history requires an exact same-run boundary")
	}
	seen := map[string]bool{}
	identities := []string{runID}
	found := false
	for _, c := range calls {
		if c.RunID != runID || tool.NormalizeSubjectKind(c.SubjectKind) != tool.SubjectChatRun {
			return ToolLedger{}, fmt.Errorf("tool history scope does not match the current chat run")
		}
		if c.ToolName == userinput.ToolName {
			if c.ID == throughID {
				return ToolLedger{}, fmt.Errorf("human answers cannot be archived as ordinary tool history")
			}
			continue
		}
		terminal := c.Status == tool.CallCompleted || c.Status == tool.CallFailed || c.Status == tool.CallDenied || c.Status == tool.CallCancelled || c.Status == tool.CallInterrupted
		if !terminal || c.Result == nil || c.ID == "" || len(c.ID) > 128 || !utf8.ValidString(c.ID) || seen[c.ID] || c.ToolName == "" || len(c.ToolName) > 128 || !utf8.ValidString(c.ToolName) {
			return ToolLedger{}, fmt.Errorf("tool history contains an unresolved or invalid record")
		}
		if c.ModelContextVersion != tool.ModelContextSnapshotVersion || c.ModelContext == "" || !utf8.ValidString(c.ModelContext) || !utf8.Valid(c.Arguments) {
			return ToolLedger{}, ErrToolLedgerSnapshotUnavailable
		}
		seen[c.ID] = true
		ledger.Entries = append(ledger.Entries, ToolLedgerEntry{CallID: c.ID, Tool: c.ToolName, Status: c.Status, ArgumentsChars: utf8.RuneCount(c.Arguments), ArgumentsSHA256: ToolLedgerHash(string(c.Arguments)), ModelContextChars: utf8.RuneCountInString(c.ModelContext), ModelContextSHA256: ToolLedgerHash(c.ModelContext)})
		ledger.Calls = append(ledger.Calls, c)
		identities = append(identities, c.ProviderCallID, c.ToolVersion, c.ContractSHA256)
		if c.ID == throughID {
			found = true
			break
		}
	}
	if !found {
		return ToolLedger{}, fmt.Errorf("tool history boundary is not present in this run")
	}
	encoded, _ := json.Marshal(struct {
		Version    int               `json:"version"`
		Identities []string          `json:"identities"`
		Entries    []ToolLedgerEntry `json:"entries"`
	}{1, identities, ledger.Entries})
	ledger.Ref.Count, ledger.Ref.SHA256 = len(ledger.Entries), ToolLedgerHash(string(encoded))
	return ledger, nil
}

// Exact Unicode slice of a saved field. Not JSON repair, a fresh resource read,
// a semantic summary, or permission to expose the full private Result.
func ToolLedgerSlice(value string, offset, maxChars int) (string, int, error) {
	total := utf8.RuneCountInString(value)
	if !utf8.ValidString(value) || offset < 0 || offset > total || maxChars < 1 || maxChars > ToolLedgerPageChars {
		return "", 0, fmt.Errorf("tool history page is outside the saved field or page limit")
	}
	start, end, n := len(value), len(value), 0
	for at := range value {
		if n == offset {
			start = at
		}
		if n == offset+maxChars {
			end = at
			break
		}
		n++
	}
	next := -1
	if offset+maxChars < total {
		next = offset + maxChars
	}
	return value[start:end], next, nil
}
