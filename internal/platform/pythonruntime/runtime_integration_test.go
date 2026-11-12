package pythonruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/platform/localexec"
)

func TestAdapterCreatesAndFreezesRealVirtualEnvironment(t *testing.T) {
	name := "python"
	if runtime.GOOS != "windows" {
		name = "python3"
	}
	python, err := exec.LookPath(name)
	if err != nil {
		t.Skip("Python 3 is not installed")
	}
	runner := localexec.NewRunner(localexec.Options{MaxOutputBytes: 512 * 1024})
	defer runner.Close()
	root := t.TempDir()
	adapter := NewWithPackageCache(runner, filepath.Join(root, "pip-cache"))
	discovery, err := adapter.Discover(context.Background(), python)
	if err != nil || len(discovery.Interpreters) == 0 {
		t.Fatalf("Discover() = %#v, %v", discovery, err)
	}
	destination := filepath.Join(root, ".staging-test")
	if err := adapter.CreateEnvironment(context.Background(), discovery.Interpreters[0].ExecutablePath, destination); err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	environmentPython := filepath.Join(destination, "bin", "python")
	if runtime.GOOS == "windows" {
		environmentPython = filepath.Join(destination, "Scripts", "python.exe")
	}
	probe, err := adapter.Probe(context.Background(), environmentPython)
	if err != nil {
		t.Fatalf("Probe(environment) error = %v", err)
	}
	if probe.Prefix == probe.BasePrefix || !probe.HasPip {
		t.Fatalf("environment probe = %#v", probe)
	}
	lock, err := adapter.Freeze(context.Background(), environmentPython)
	if err != nil {
		t.Fatalf("Freeze() error = %v", err)
	}
	if len(lock) == 0 {
		t.Fatal("Freeze() returned no base packages")
	}
	if runtime.GOOS == "windows" {
		deepApp := NewWithPackageCache(runner, filepath.Join(root, strings.Repeat("application-cache-component-", 4), "python-packages"))
		if err := deepApp.InstallPackages(context.Background(), environmentPython, []string{"--no-index", "--no-deps", writeOfflinePathFixtureSDist(t, root)}); err != nil {
			t.Fatal("deep application root regressed the shorter external environment cache", err)
		}
		if _, err := os.Stat(filepath.Join(destination, ".pip-cache", "wheels")); err != nil {
			t.Fatal("shorter workspace cache was not actually used", err)
		}
		// A short alias must not disguise a deep cache target. pip resolves
		// junctions before building wheels, so the host must do the same.
		deepTarget := filepath.Join(root, strings.Repeat("deep-cache-target-", 5))
		if err := os.MkdirAll(deepTarget, 0700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(root, "c")
		if out, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", alias, deepTarget).CombinedOutput(); err != nil {
			t.Fatalf("create controlled cache junction: %v %s", err, out)
		}
		defer os.Remove(alias)
		aliasedApp := NewWithPackageCache(runner, filepath.Join(alias, "p"))
		if err := aliasedApp.InstallPackages(context.Background(), environmentPython, []string{"--no-index", "--no-deps", "--force-reinstall", writeOfflinePathFixtureSDist(t, root)}); err != nil {
			t.Fatal("short application alias displaced an actually shorter environment cache", err)
		}
		if _, err := os.Stat(filepath.Join(deepTarget, "p", "wheels")); !os.IsNotExist(err) {
			t.Fatal("deep aliased application cache was unexpectedly used", err)
		}
	}
	if err := adapter.ValidateEnvironmentScripts(context.Background(), environmentPython); err == nil {
		t.Fatal("staging path must not pass published script validation")
	}
	// A local synthetic wheel's metadata exercises non-pip console entry points
	// without network access, package installation or touching user environments.
	output, err := exec.Command(environmentPython, "-I", "-c", "import sysconfig; print(sysconfig.get_path('purelib'))").Output()
	if err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(strings.TrimSpace(string(output)), "sciaide_cli_test-1.0.dist-info")
	if err := os.Mkdir(dist, 0700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"METADATA": "Metadata-Version: 2.1\nName: sciaide-cli-test\nVersion: 1.0\n", "entry_points.txt": "[console_scripts]\nsciaide-json-test = json.tool:main\n", "RECORD": ""} {
		if err := os.WriteFile(filepath.Join(dist, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	published := filepath.Join(root, "venv")
	if err := os.Rename(destination, published); err != nil {
		t.Fatal(err)
	}
	environmentPython = filepath.Join(published, "bin", "python")
	if runtime.GOOS == "windows" {
		environmentPython = filepath.Join(published, "Scripts", "python.exe")
	}
	if err := adapter.ValidateEnvironmentScripts(context.Background(), environmentPython); err == nil {
		t.Fatal("renamed venv unexpectedly passes script validation")
	}
	if err := adapter.FinalizeEnvironment(context.Background(), environmentPython, destination); err != nil {
		t.Fatalf("FinalizeEnvironment: %v", err)
	}
	if err := adapter.ValidateEnvironmentScripts(context.Background(), environmentPython); err != nil {
		t.Fatalf("published script validation: %v", err)
	}
	for _, name := range []string{"pip", "pip3", "pip" + strings.Join(strings.Split(probe.Version, ".")[:2], ".")} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if out, err := exec.Command(filepath.Join(filepath.Dir(environmentPython), name), "--version").CombinedOutput(); err != nil {
			t.Fatalf("pip alias %s: %v: %s", name, err, out)
		}
	}
	cli := filepath.Join(filepath.Dir(environmentPython), "sciaide-json-test")
	if runtime.GOOS == "windows" {
		cli += ".exe"
	}
	if out, err := exec.Command(cli, "--help").CombinedOutput(); err != nil {
		t.Fatalf("generated package CLI: %v: %s", err, out)
	}
	// Execute activation as a user would, not just a textual match.
	var activate *exec.Cmd
	if runtime.GOOS == "windows" {
		batch := "@echo off\r\ncall \"" + filepath.Join(published, "Scripts", "activate.bat") + "\"\r\npython -c \"import sys; print(sys.prefix)\"\r\n"
		if err := os.WriteFile(filepath.Join(root, "test-activate.cmd"), []byte(batch), 0600); err != nil {
			t.Fatal(err)
		}
		activate = exec.Command("cmd.exe", "/d", "/c", "test-activate.cmd")
		activate.Dir = root
	} else {
		activate = exec.Command("sh", "-c", `. "$1"; python -c 'import sys; print(sys.prefix)'`, "sh", filepath.Join(published, "bin", "activate"))
	}
	out, err := activate.CombinedOutput()
	if err != nil {
		t.Fatalf("activation: %v: %s", err, out)
	}
	// Windows can canonicalize path casing; compare directory identity, not spelling.
	actual := strings.TrimSpace(string(out))
	actualInfo, actualErr := os.Stat(actual)
	expectedInfo, expectedErr := os.Stat(published)
	if actualErr != nil || expectedErr != nil || !os.SameFile(actualInfo, expectedInfo) {
		t.Fatalf("activation prefix: actual=%q expected=%q; stat errors: %v, %v", actual, published, actualErr, expectedErr)
	}
	activationName := "activate"
	if runtime.GOOS == "windows" {
		activationName = "activate.bat"
	}
	activationPath := filepath.Join(filepath.Dir(environmentPython), activationName)
	activationBefore, err := os.ReadFile(activationPath)
	if err != nil {
		t.Fatal(err)
	}
	assertReadOnlyValidation := func(t *testing.T, selected string) {
		t.Helper()
		if err := adapter.ValidateEnvironmentScripts(context.Background(), selected); err != nil {
			t.Fatalf("existing environment through another spelling: %v", err)
		}
		after, err := os.ReadFile(activationPath)
		if err != nil || !bytes.Equal(activationBefore, after) {
			t.Fatal("read-only validation modified legacy activation", err)
		}
	}
	t.Run("workspace-alias-keeps-existing-activation", func(t *testing.T) {
		alias := filepath.Join(root, "workspace-alias")
		if runtime.GOOS == "windows" {
			// Directory junctions do not require Windows symlink privileges. Both
			// endpoints are in this test's own temporary root.
			if out, junctionErr := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", alias, published).CombinedOutput(); junctionErr != nil {
				t.Skipf("directory alias is unavailable: %v: %s", junctionErr, out)
			}
		} else if err := os.Symlink(published, alias); err != nil {
			t.Skipf("directory symlink is unavailable: %v", err)
		}
		defer os.Remove(alias) // Remove only the alias, never the target tree.
		relativePython, err := filepath.Rel(published, environmentPython)
		if err != nil {
			t.Fatal(err)
		}
		selected := filepath.Join(alias, relativePython)
		if _, err := os.Stat(selected); err != nil {
			t.Fatalf("workspace alias %q does not resolve selected executable %q: %v", alias, selected, err)
		}
		assertReadOnlyValidation(t, selected)
		wrongRoot := alias + "-old"
		if err := os.Mkdir(wrongRoot, 0700); err != nil {
			t.Fatal(err)
		}
		wrongActivation := bytes.ReplaceAll(activationBefore, []byte(published), []byte(wrongRoot))
		if bytes.Equal(wrongActivation, activationBefore) {
			t.Fatal("negative fixture did not replace the actual activation root")
		}
		if err := os.WriteFile(activationPath, wrongActivation, 0600); err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(activationPath, activationBefore, 0600)
		if err := adapter.ValidateEnvironmentScripts(context.Background(), selected); err == nil {
			t.Fatal("accepted a different activation directory with the same path prefix")
		}
	})
	if runtime.GOOS == "windows" {
		t.Run("path-casing-keeps-existing-activation", func(t *testing.T) {
			assertReadOnlyValidation(t, strings.ToUpper(environmentPython))
		})
	}
	t.Run("shell-expansion-is-not-a-literal-activation-path", func(t *testing.T) {
		unsafeRoot := filepath.Join(root, "venv$HOME")
		if runtime.GOOS == "windows" {
			unsafeRoot = filepath.Join(root, "venv%TEMP%")
		}
		unsafeActivation := bytes.ReplaceAll(activationBefore, []byte(published), []byte(unsafeRoot))
		if err := os.WriteFile(activationPath, unsafeActivation, 0600); err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(activationPath, activationBefore, 0600)
		if err := adapter.ValidateEnvironmentScripts(context.Background(), environmentPython); err == nil || !strings.Contains(err.Error(), "unsafe activation") {
			t.Fatal("did not reject shell expansion before trusting the literal directory", err)
		}
	})
	// Reject path traversal metadata before invoking distlib or writing scripts.
	newDist := filepath.Join(published, strings.TrimPrefix(dist, destination+string(filepath.Separator)))
	if err := os.WriteFile(filepath.Join(newDist, "entry_points.txt"), []byte("[console_scripts]\n../escape = json.tool:main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.FinalizeEnvironment(context.Background(), environmentPython, destination); err == nil {
		t.Fatal("accepted unsafe entry point name")
	}
}

func TestAdapterLongWindowsEnvironmentBootstrapsBundledPip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows MAX_PATH regression")
	}
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("Python is unavailable")
	}
	runner := localexec.NewRunner(localexec.Options{MaxOutputBytes: 512 * 1024})
	defer runner.Close()
	root := t.TempDir()
	packageCache := filepath.Join(root, "pip-cache")
	adapter := NewWithPackageCache(runner, packageCache)
	parent := filepath.Join(root, "中文 project with spaces")
	for len(parent) < 185 {
		parent = filepath.Join(parent, strings.Repeat("p", 20))
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, ".staging-long-path")
	if err := adapter.CreateEnvironment(context.Background(), python, destination); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(destination, "Scripts", "python.exe")
	probe, err := adapter.Probe(context.Background(), executable)
	if err != nil || !probe.HasPip || probe.Prefix == probe.BasePrefix || strings.HasPrefix(probe.Prefix, `\\?\`) {
		t.Fatal("long environment isolation or logical identity failed", err)
	}
	if lock, err := adapter.Freeze(context.Background(), executable); err != nil || len(lock) == 0 {
		t.Fatal("long environment freeze failed", err)
	}
	// Exercise pip's persistent wheel-cache descendants, not just ensurepip.
	// The sdist contains its own stdlib-only PEP 517 backend and needs no index,
	// third-party builder, global package or network access.
	archive := writeOfflinePathFixtureSDist(t, root)
	if err := adapter.InstallPackages(context.Background(), executable, []string{"--no-index", "--no-deps", archive}); err != nil {
		t.Fatal("long-path offline sdist install failed", err)
	}
	cacheWheel := false
	if err := filepath.WalkDir(filepath.Join(packageCache, "wheels"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".whl") {
			if len(filepath.Join(destination, ".pip-cache", strings.TrimPrefix(path, packageCache+string(filepath.Separator)))) <= 260 {
				t.Fatal("fixture would not exceed MAX_PATH with the old per-venv wheel cache")
			}
			cacheWheel = true
		}
		return nil
	}); err != nil || !cacheWheel {
		t.Fatal("offline install did not exercise the application-scoped persistent wheel cache", err)
	}
	if out, err := exec.Command(CommandPath(executable), "-I", "-c", "import sciaide_path_fixture; print(sciaide_path_fixture.VALUE)").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "offline-path-fixture" {
		t.Fatalf("offline sdist import failed: %v %s", err, out)
	}
	published := filepath.Join(parent, "venv")
	if err := os.Rename(destination, published); err != nil {
		t.Fatal(err)
	}
	executable = filepath.Join(published, "Scripts", "python.exe")
	if err := adapter.FinalizeEnvironment(context.Background(), executable, destination); err != nil {
		t.Fatal(err)
	}
	if err := adapter.ValidateEnvironmentScripts(context.Background(), executable); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(CommandPath(filepath.Join(published, "Scripts", "pip.exe")), "--version").CombinedOutput(); err != nil {
		t.Fatalf("published long-path pip launcher failed: %v %s", err, out)
	}
	if out, err := exec.Command(CommandPath(filepath.Join(published, "Scripts", "sciaide-path-fixture.exe")), "--help").CombinedOutput(); err != nil {
		t.Fatalf("published long-path package launcher failed: %v %s", err, out)
	}
}

func writeOfflinePathFixtureSDist(t *testing.T, root string) string {
	t.Helper()
	backend := `import base64, hashlib, os, zipfile
def get_requires_for_build_wheel(config_settings=None):
    return []
def build_wheel(wheel_directory, config_settings=None, metadata_directory=None):
    name = 'sciaide_path_fixture-1.0-py3-none-any.whl'
    prefix = 'sciaide_path_fixture-1.0.dist-info/'
    files = {
        'sciaide_path_fixture.py': b"VALUE = 'offline-path-fixture'\n",
        prefix + 'METADATA': b'Metadata-Version: 2.1\nName: sciaide-path-fixture\nVersion: 1.0\n',
        prefix + 'WHEEL': b'Wheel-Version: 1.0\nGenerator: sciaide-offline-fixture\nRoot-Is-Purelib: true\nTag: py3-none-any\n',
        prefix + 'entry_points.txt': b'[console_scripts]\nsciaide-path-fixture = json.tool:main\n',
    }
    rows = []
    for path, data in files.items():
        digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b'=').decode('ascii')
        rows.append(path + ',sha256=' + digest + ',' + str(len(data)))
    rows.append(prefix + 'RECORD,,')
    files[prefix + 'RECORD'] = ('\n'.join(rows) + '\n').encode('utf-8')
    with zipfile.ZipFile(os.path.join(wheel_directory, name), 'w') as stream:
        for path, data in files.items():
            stream.writestr(path, data)
    return name
`
	archive := filepath.Join(root, "sciaide_path_fixture-1.0.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tarball := tar.NewWriter(gz)
	for _, item := range []struct{ name, contents string }{{"pyproject.toml", "[build-system]\nrequires = []\nbuild-backend = 'fixture_backend'\nbackend-path = ['.']\n"}, {"fixture_backend.py", backend}} {
		if err := tarball.WriteHeader(&tar.Header{Name: "sciaide_path_fixture-1.0/" + item.name, Mode: 0600, Size: int64(len(item.contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarball.Write([]byte(item.contents)); err != nil {
			t.Fatal(err)
		}
	}
	for _, close := range []func() error{tarball.Close, gz.Close, file.Close} {
		if err := close(); err != nil {
			t.Fatal(err)
		}
	}
	return archive
}
