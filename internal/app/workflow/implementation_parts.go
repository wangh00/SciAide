package workflow

import (
	"encoding/json"
	"fmt"
)

// Transport subdivision, not a new research stage or a scientific shortcut.
const ImplementationPartsInstruction = `

Incremental implementation transport:
Use submit_implementation_part for source transport when it is exposed. Do not emit Markdown code fences or a separate metadata-only response. After necessary resource reads, use that control function exactly once per response to supply one ordered source part, complete=false until the final part. Aim for about 80-150 lines per response, finishing at a logical source boundary. This is not a request to shorten the method or omit checks. The FIRST call must contain index, code (nonempty Python source), complete, and metadata TOGETHER. Put all scientific fields, including dependencies, inside metadata, never at the argument root. Subsequent parts contain index, code and complete only; they must not change or repeat metadata. Use the exact offered index. Append only new source after the already saved prefix; do not regenerate imports or previous functions. Preserve source whitespace and finish non-final parts with a newline. The final part includes the remaining outputs and the final result expression and sets complete=true. Do not execute code, tools or IO through this control function. SciAide saves only completed responses, assembles the source, then applies the unchanged full stage Schema, scientific checks and Kernel contract. A partial draft is never an accepted implementation.
The first metadata object must include every required property, including dependencies even when packages are already installed. A format rejection saves NONE of that submission and does NOT advance the index: resubmit the ENTIRE corrected unsaved part, including its code and complete metadata for index 1, not just changed fields. Source from the rejected candidate is NOT a saved prefix; retain it in the corrected submission unless the diagnostic requires a source change. The saved source prefix and completed reads remain unchanged. The host permits two format corrections for the current part, separately from network recovery and full scientific validation.
`

const ImplementationPatchInstruction = `
Technical Python repair transport:
When submit_implementation_patch is offered, prefer a small exact replacement against the host's frozen previously executed source instead of resubmitting unchanged code and metadata. Choose either a technical patch or a full source-part replacement, never both in one response. Supply baseCodeSha256 exactly as offered, replacements with unique exact old substrings and replacement new text, and an explicit researchChanges declaration. All old substrings refer to the original source, not to earlier replacements. Include enough surrounding context to disambiguate repeated text. Do not repeat the full code or dependencies: the host retains the original complete metadata and applies the patch atomically, then runs the unchanged full stage Schema, scientific checks, dependency preparation, Kernel and independent review.
Use researchChanges=[] only for a technical fix that preserves the agreed scientific method. Changes to dependencies, analysis parameters, assumptions, limitations, outputs or scientific decisions require a complete replacement through submit_implementation_part, with complete metadata and declared researchChanges, rather than hiding them in a technical patch. A source patch is not execution or proof of scientific validity. Do not invoke IO during this repair exchange. If a patch is rejected, correct only the patch against the same frozen base; no partial replacement is saved or executed.
`

func ImplementationMetadataSchema(schema json.RawMessage) (json.RawMessage, error) {
	// Derived from the frozen contract, never from a provider supplied schema.
	if err := ValidateAIStageSchema(schema); err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	var properties map[string]json.RawMessage
	var required []string
	if json.Unmarshal(schema, &root) != nil || json.Unmarshal(root["properties"], &properties) != nil || properties["code"] == nil || json.Unmarshal(root["required"], &required) != nil {
		return nil, fmt.Errorf("implementation contract has no code property")
	}
	delete(properties, "code")
	filtered := []string{}
	for _, name := range required {
		if name != "code" {
			filtered = append(filtered, name)
		}
	}
	root["properties"], _ = json.Marshal(properties)
	root["required"], _ = json.Marshal(filtered)
	value, err := json.Marshal(root)
	if err == nil {
		err = ValidateAIStageSchema(value)
	}
	return value, err
}
