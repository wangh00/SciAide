package localexec

import (
	"os"
	"runtime"
	"sort"
	"strings"
)

var windowsCoreEnvironment = []string{
	"PATH", "PATHEXT", "SHELL", "COMSPEC", "SYSTEMROOT", "SYSTEMDRIVE",
	"USERNAME", "USERDOMAIN", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
	"PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMW6432", "PROGRAMDATA",
	"LOCALAPPDATA", "APPDATA", "TEMP", "TMP", "TMPDIR", "POWERSHELL", "PWSH",
}

var unixCoreEnvironment = []string{
	"PATH", "SHELL", "TMPDIR", "TEMP", "TMP", "HOME", "LANG", "LC_ALL",
	"LC_CTYPE", "LOGNAME", "USER",
}

// BuildEnvironment constructs a minimal child environment from an allowlist.
// It never starts by inheriting the entire SciAide process environment.
func BuildEnvironment(source []string, overrides map[string]string) ([]string, []string) {
	allowed := unixCoreEnvironment
	if runtime.GOOS == "windows" {
		allowed = windowsCoreEnvironment
	}
	values := make(map[string]string, len(allowed)+len(overrides))
	canonical := make(map[string]string, len(allowed)+len(overrides))
	for _, item := range source {
		name, value, ok := splitEnvironment(item)
		if !ok || !containsFold(allowed, name) || sensitiveEnvironmentName(name) {
			continue
		}
		key := strings.ToUpper(name)
		values[key], canonical[key] = value, name
	}
	if runtime.GOOS == "windows" {
		if _, exists := values["PATHEXT"]; !exists {
			values["PATHEXT"], canonical["PATHEXT"] = ".COM;.EXE;.BAT;.CMD", "PATHEXT"
		}
	}
	for name, value := range overrides {
		name = strings.TrimSpace(name)
		if name == "" || strings.IndexByte(name, '=') >= 0 || strings.IndexByte(name, 0) >= 0 || sensitiveEnvironmentName(name) {
			continue
		}
		key := strings.ToUpper(name)
		values[key], canonical[key] = value, name
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		name := canonical[key]
		environment = append(environment, name+"="+values[key])
		names = append(names, name)
	}
	return environment, names
}

func CoreEnvironment(overrides map[string]string) ([]string, []string) {
	return BuildEnvironment(os.Environ(), overrides)
}

func splitEnvironment(value string) (string, string, bool) {
	separator := strings.IndexByte(value, '=')
	if separator <= 0 {
		return "", "", false
	}
	return value[:separator], value[separator+1:], true
}

func sensitiveEnvironmentName(name string) bool {
	upper := strings.ToUpper(name)
	return strings.Contains(upper, "KEY") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "TOKEN")
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
