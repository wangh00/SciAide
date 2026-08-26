package localexec

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestBuildEnvironmentUsesCoreAllowlistAndDropsSecrets(t *testing.T) {
	source := []string{
		"PATH=C:\\Tools", "SystemRoot=C:\\Windows", "OPENAI_API_KEY=secret",
		"SESSION_TOKEN=secret", "DATABASE_PASSWORD=secret", "UNRELATED=value",
	}
	values, names := BuildEnvironment(source, map[string]string{
		"PYTHONUTF8": "1", "CUSTOM_SECRET": "refused",
	})
	joined := strings.Join(values, "\n")
	for _, secret := range []string{"OPENAI_API_KEY", "SESSION_TOKEN", "CUSTOM_SECRET", "secret"} {
		if strings.Contains(strings.ToUpper(joined), strings.ToUpper(secret)) {
			t.Fatalf("sensitive environment leaked: %s", joined)
		}
	}
	if !strings.Contains(joined, "PYTHONUTF8=1") {
		t.Fatalf("safe override missing: %s", joined)
	}
	if runtime.GOOS == "windows" {
		if !slices.ContainsFunc(names, func(value string) bool { return strings.EqualFold(value, "PATH") }) || !strings.Contains(strings.ToUpper(joined), "SYSTEMROOT=C:\\WINDOWS") {
			t.Fatalf("Windows core environment missing: %s", joined)
		}
	} else if strings.Contains(joined, "SystemRoot") {
		t.Fatalf("Windows variable leaked on non-Windows: %s", joined)
	}
}
