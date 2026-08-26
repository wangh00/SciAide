package main

import (
	"errors"
	"strings"
	"testing"
)

func TestStartupErrorMessageIncludesCause(t *testing.T) {
	message := startupErrorMessage(errors.New("migration 54 checksum changed"))
	if !strings.Contains(message, "SciAide 无法启动") || !strings.Contains(message, "migration 54 checksum changed") {
		t.Fatalf("startupErrorMessage() = %q", message)
	}
}
