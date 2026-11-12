package chat

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Exact replacements against one frozen, previously executed source. These
// are data, never file operations or executable repair instructions.
type ImplementationReplacement struct {
	Old string `json:"old"`
	New string `json:"new"`
}

type ImplementationSourcePatch struct {
	BaseCodeSHA256 string                      `json:"baseCodeSha256"`
	Replacements   []ImplementationReplacement `json:"replacements"`
}

type ImplementationPatchBase struct {
	Metadata json.RawMessage
	Code     string
}

func ImplementationCodeSHA256(code string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(code)))
}

// Only a host-frozen automatic repair can offer a patch. Provider-authored
// metadata or conversation text never chooses the source to be rewritten.
func FrozenImplementationPatchBase(contract WorkflowAIExecution) (ImplementationPatchBase, bool, error) {
	if contract.NodeID != "method_implementation" || !strings.Contains(contract.PromptVersion, ImplementationPatchVersion) {
		return ImplementationPatchBase{}, false, nil
	}
	if !json.Valid(contract.Input) || ImplementationCodeSHA256(string(contract.Input)) != contract.InputSHA256 {
		return ImplementationPatchBase{}, false, fmt.Errorf("implementation repair input is not the frozen execution input")
	}
	var input struct {
		Repair *struct {
			Prior json.RawMessage `json:"priorImplementation"`
		} `json:"_pythonRepair"`
	}
	if err := json.Unmarshal(contract.Input, &input); err != nil {
		return ImplementationPatchBase{}, false, err
	}
	if input.Repair == nil {
		return ImplementationPatchBase{}, false, nil
	}
	var prior map[string]json.RawMessage
	if err := json.Unmarshal(input.Repair.Prior, &prior); err != nil || prior == nil {
		return ImplementationPatchBase{}, false, fmt.Errorf("implementation repair has no frozen prior result")
	}
	var code string
	if json.Unmarshal(prior["code"], &code) != nil || strings.TrimSpace(code) == "" || len(code) > 120_000 || !utf8.ValidString(code) {
		return ImplementationPatchBase{}, false, fmt.Errorf("implementation repair has no complete prior source")
	}
	delete(prior, "code")
	// A previous declaration described that prior generation's decisions.
	// It cannot be re-used as the new generation's declaration of changes.
	prior["researchChanges"] = json.RawMessage(`[]`)
	metadata, err := json.Marshal(prior)
	return ImplementationPatchBase{Metadata: metadata, Code: code}, true, err
}

// Match every replacement against the original base before applying any.
// No fuzzy match, global replacement, cascading edits or partial publication.
func ApplyImplementationSourcePatch(base string, patch ImplementationSourcePatch) (string, error) {
	if patch.BaseCodeSHA256 != ImplementationCodeSHA256(base) || len(patch.Replacements) < 1 || len(patch.Replacements) > 24 {
		return "", fmt.Errorf("patch base digest or replacement count does not match")
	}
	type edit struct {
		start, end int
		text       string
	}
	edits := make([]edit, 0, len(patch.Replacements))
	for i, replacement := range patch.Replacements {
		if replacement.Old == "" || replacement.Old == replacement.New || !utf8.ValidString(replacement.Old) || !utf8.ValidString(replacement.New) || len(replacement.Old) > 120_000 || len(replacement.New) > 120_000 {
			return "", fmt.Errorf("replacement %d must contain a nonempty old string and a real UTF-8 change", i+1)
		}
		start := strings.Index(base, replacement.Old)
		if start < 0 || strings.Index(base[start+1:], replacement.Old) >= 0 {
			return "", fmt.Errorf("replacement %d old text must match exactly once in frozen source; include surrounding context", i+1)
		}
		edits = append(edits, edit{start, start + len(replacement.Old), replacement.New})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out strings.Builder
	previous := 0
	for _, edit := range edits {
		if edit.start < previous {
			return "", fmt.Errorf("patch replacements overlap in frozen source")
		}
		out.WriteString(base[previous:edit.start])
		out.WriteString(edit.text)
		previous = edit.end
	}
	out.WriteString(base[previous:])
	if strings.TrimSpace(out.String()) == "" || out.Len() > 120_000 {
		return "", fmt.Errorf("patched source is empty or exceeds the original source contract")
	}
	return out.String(), nil
}
