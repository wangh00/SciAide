package pythonruntime

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/wangh00/SciAide/internal/platform/localexec"
)

// Venv launchers contain absolute interpreter paths. Recreate their source
// using the same standard tooling pip/venv use, never patch executable bytes.
const environmentScriptsScript = `import base64, csv, hashlib, importlib.metadata as metadata, os, pathlib, re, subprocess, sys, sysconfig, venv
root = pathlib.Path(sys.prefix).resolve()
if sys.prefix == sys.base_prefix:
    raise RuntimeError('script validation requires an isolated virtual environment')
scripts = pathlib.Path(sysconfig.get_path('scripts')).resolve()
def within(path):
    path = pathlib.Path(path)
    if path.is_symlink():
        raise RuntimeError('refusing a symlinked script or metadata file: ' + str(path))
    path = path.resolve()
    if path != root and root not in path.parents:
        raise RuntimeError('script metadata escapes the virtual environment: ' + str(path))
    return path
within(scripts)
repair = sys.argv[1] == 'repair'
old = sys.argv[2] if len(sys.argv) > 2 else ''
if repair:
    from pip._vendor.distlib.scripts import ScriptMaker
    for existing in scripts.iterdir():
        if not existing.name.lower().startswith('python'):
            within(existing)
    jobs = []
    owners = set()
    # Validate every entry before writing any generated script. Entry point
    # objects are data only: never call EntryPoint.load().
    for dist in metadata.distributions():
        entries = [ep for ep in dist.entry_points if ep.group in ('console_scripts', 'gui_scripts')]
        if not entries:
            continue
        record = within(pathlib.Path(dist._path) / 'RECORD')
        if not record.is_file():
            raise RuntimeError('entry point distribution has no wheel RECORD; rebuild required')
        specs = []
        for ep in entries:
            if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]*', ep.name) or ep.name.lower().startswith(('python', 'activate', 'deactivate')):
                raise RuntimeError('unsafe console script name: ' + ep.name)
            value = ep.value.split('[', 1)[0].strip()
            if not re.fullmatch(r'[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*\s*:\s*[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*', value):
                raise RuntimeError('invalid console script entry point: ' + ep.name)
            names = [ep.name]
            if dist.metadata['Name'].lower() == 'pip' and ep.name == 'pip':
                names += ['pip' + str(sys.version_info.major), 'pip%d.%d' % sys.version_info[:2]]
            for name in names:
                key = os.path.normcase(name)
                if key in owners:
                    # pip aliases may also be explicitly declared by pip.
                    if dist.metadata['Name'].lower() == 'pip' and name.startswith('pip'):
                        continue
                    raise RuntimeError('duplicate console script owner: ' + name)
                owners.add(key)
                for suffix in ('', '.exe', '-script.py', '-script.pyw'):
                    within(scripts / (name + suffix))
                specs.append((name + ' = ' + value, ep.group == 'gui_scripts'))
        jobs.append((dist, record, specs))
    builder = venv.EnvBuilder(with_pip=False)
    context = builder.ensure_directories(str(root))
    builder.setup_scripts(context)
    for dist, record, specs in jobs:
        maker = ScriptMaker(None, str(scripts))
        maker.executable = sys.executable
        maker.clobber = True
        maker.variants = {''}
        maker.set_mode = True
        generated = []
        for spec, gui in specs:
            generated += maker.make(spec, {'gui': gui})
        # Keep wheel installation metadata consistent with regenerated files.
        with record.open(newline='', encoding='utf-8') as stream:
            rows = list(csv.reader(stream))
        replacement = {}
        for path in generated:
            path = within(path)
            data = path.read_bytes()
            rel = os.path.relpath(path, dist.locate_file('')).replace(os.sep, '/')
            digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b'=').decode('ascii')
            replacement[rel] = [rel, 'sha256=' + digest, str(len(data))]
        rows = [row for row in rows if row and row[0] not in replacement] + list(replacement.values())
        with record.open('w', newline='', encoding='utf-8') as stream:
            csv.writer(stream).writerows(rows)
# Detect old staged paths in both text activation files and binary launchers.
# This is read-only; arbitrary legacy scripts are never modified speculatively.
for path in scripts.iterdir():
    if not path.is_file() or path.name.lower().startswith('python'):
        continue
    data = within(path).read_bytes()
    if b'.staging-' in data or (old and os.fsencode(old) in data):
        raise RuntimeError('virtual environment scripts still reference an old staging path; rebuild required: ' + path.name)
activation = scripts / ('activate.bat' if os.name == 'nt' else 'activate')
if not activation.is_file() or os.fsencode(str(root)) not in activation.read_bytes():
    raise RuntimeError('virtual environment activation points to a different location; rebuild required')
pip = scripts / ('pip.exe' if os.name == 'nt' else 'pip')
result = subprocess.run([str(pip), '--version'], capture_output=True, timeout=30)
if result.returncode != 0:
    raise RuntimeError('virtual environment pip launcher is unusable; rebuild required')
print('environment scripts verified')
`

func (a *Adapter) FinalizeEnvironment(ctx context.Context, environmentPythonPath, previousRoot string) error {
	return a.environmentScripts(ctx, environmentPythonPath, "repair", previousRoot)
}

func (a *Adapter) ValidateEnvironmentScripts(ctx context.Context, environmentPythonPath string) error {
	return a.environmentScripts(ctx, environmentPythonPath, "verify", "")
}

func (a *Adapter) environmentScripts(ctx context.Context, pythonPath, mode, previousRoot string) error {
	pythonPath, err := secureExecutablePath(pythonPath)
	if err != nil {
		return err
	}
	environment, _ := localexec.CoreEnvironment(map[string]string{"PYTHONIOENCODING": "utf-8", "PYTHONUTF8": "1", "PIP_DISABLE_PIP_VERSION_CHECK": "1", "NO_COLOR": "1"})
	result, runErr := a.runner.Execute(ctx, localexec.Request{Program: pythonPath, Args: []string{"-I", "-u", "-X", "utf8", "-c", environmentScriptsScript, mode, previousRoot}, Dir: filepath.Dir(pythonPath), Env: environment, Timeout: time.Minute})
	if runErr != nil || result.ExitCode != 0 {
		return executionError("validate/repair managed Python scripts (rebuild the environment if needed)", result, runErr)
	}
	if result.Stdout.Truncated || result.Stderr.Truncated {
		return fmt.Errorf("Python script validation output exceeded the configured limit")
	}
	return nil
}
