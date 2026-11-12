package modelutil

import "strings"

// Some nonstandard gateways send a successful text response containing ONLY
// an operational error marker. Recognize only observed exact identities, never
// a code mentioned in prose, quotes, fenced examples or a scientific result.
func StandaloneGatewayError(text string) error {
	value := strings.TrimSpace(text)
	if !strings.HasPrefix(value, "[error:") || !strings.HasSuffix(value, "]") {
		return nil
	}
	code := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "[error:"), "]"))
	switch code {
	case "usage_limit_reached", "insufficient_quota", "quota_exceeded", "billing_hard_limit_reached", "no_userid":
		// Fingerprint the actual returned text, not a fabricated JSON envelope.
		// Only the known code survives into public diagnostics; never the text.
		metadata := streamEventMetadata([]byte(text))
		metadata["type"] = "error"
		metadata["providerCode"] = code
		details := ProviderErrorDetails("standalone gateway error marker", 0, encodedProviderMetadata(metadata))
		return ClassifyStreamError(code, code, "", details)
	}
	return nil
}
