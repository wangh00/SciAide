package workflow

import (
	"encoding/json"
	"sort"
	"strings"
)

const reviewKernelPathVersion = "review-kernel-path-v1"

const kernelReviewNavigationInstruction = `
Kernel evidence navigation: the host inventory supplies executionEnvelopePointer, valuePointer and bodyFieldPointers for actual Kernel-shaped inputs. The execution envelope contains status, stdout and provenance; the scientific result body is under its value field. Start a scientific basis pointer at the supplied valuePointer and append every actual nested key/index, including any further value fields. Never omit /value or infer paths from a prior stage's flattened summary. Quotes must be exact scalar text (continuous, not paraphrased), or the exact JSON number/boolean at that pointer. This inventory is navigation, not evidence or a review verdict; read the unchanged stage_input body and independently check each claim.
`

type reviewKernelLocation struct {
	ExecutionEnvelopePointer string   `json:"executionEnvelopePointer"`
	ValuePointer             string   `json:"valuePointer"`
	BodyFieldPointers        []string `json:"bodyFieldPointers"`
}

// These two workflow ports carry Kernel envelopes. Never recursively search
// arbitrary source data for something called "value" or flatten user objects.
var reviewKernelRoots = []string{"computedResults", "dataPreflight"}

func reviewKernelBody(input map[string]any, key string) (map[string]any, bool) {
	envelope, ok := input[key].(map[string]any)
	if !ok || envelope["status"] != "success" || envelope["valueType"] != "dict" {
		return nil, false
	}
	for _, field := range []string{"kernelId", "executionId", "codeSha256", "environmentFingerprint"} {
		text, ok := envelope[field].(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false
		}
	}
	body, ok := envelope["value"].(map[string]any)
	return body, ok
}

func reviewKernelLocations(input json.RawMessage) []reviewKernelLocation {
	root := decodeObject(input)
	locations := []reviewKernelLocation{}
	for _, key := range reviewKernelRoots {
		body, ok := reviewKernelBody(root, key)
		if !ok {
			continue
		}
		location := reviewKernelLocation{ExecutionEnvelopePointer: "/" + key, ValuePointer: "/" + key + "/value", BodyFieldPointers: []string{}}
		for field := range body {
			escaped := strings.ReplaceAll(strings.ReplaceAll(field, "~", "~0"), "/", "~1")
			location.BodyFieldPointers = append(location.BodyFieldPointers, location.ValuePointer+"/"+escaped)
		}
		sort.Strings(location.BodyFieldPointers)
		locations = append(locations, location)
	}
	return locations
}

func canonicalReviewBasisTarget(input map[string]any, pointer string) (target, rule string) {
	if strings.HasPrefix(pointer, "/indexedReadRanges/") {
		if _, exists := input["indexedReadRanges"]; !exists {
			return "/researchSourceContext" + pointer, "frozen_read_range_path"
		}
		return "", ""
	}
	// An existing path wins even if its quote is wrong: it is a scientific
	// conflict to diagnose, not an excuse to silently choose another value.
	if _, _, err := reviewBasisAt(input, pointer); err == nil {
		return "", ""
	}
	for _, key := range reviewKernelRoots {
		prefix := "/" + key + "/"
		if !strings.HasPrefix(pointer, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(pointer, prefix)
		if suffix == "value" || strings.HasPrefix(suffix, "value/") {
			return "", ""
		}
		if _, ok := reviewKernelBody(input, key); ok {
			return prefix + "value/" + suffix, "frozen_kernel_value_path"
		}
	}
	return "", ""
}

func reviewBasisQuoteMatches(value any, quote string) bool {
	if strings.TrimSpace(quote) == "" {
		return false
	}
	switch scalar := value.(type) {
	case string:
		return strings.Contains(scalar, quote)
	case json.Number, float64, bool:
		return string(mustJSON(value)) == quote
	}
	return false
}
