package modelutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/apperr"
)

// HTTPGatewayIdentityError handles only the observed, explicit error.code.
// It must run before HTTP-status retries or optional-parameter negotiation.
// Unknown, ambiguous and malformed identities retain their existing handling.
func HTTPGatewayIdentityError(status int, body []byte) error {
	if status < 400 {
		return nil
	}
	root, ok := gatewayIdentityObject(body)
	if !ok {
		return nil
	}
	failure, ok := gatewayIdentityObject(root["error"])
	if !ok {
		return nil
	}
	var code string
	if json.Unmarshal(failure["code"], &code) != nil || code != "no_userid" {
		return nil
	}
	details := ProviderErrorDetails("HTTP response", status, encodedProviderMetadata(streamEventMetadata(body)))
	err := ClassifyStreamError("", code, "", details)
	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		appErr.HTTPStatus = status
		appErr.Details = details
	}
	return err
}

func gatewayIdentityObject(raw []byte) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	seen := make(map[string]bool)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[strings.ToLower(key)] {
			return nil, false
		}
		seen[strings.ToLower(key)] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	return fields, true
}
