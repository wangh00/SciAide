package pythonkernel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/platform/localexec"
)

const (
	maxFrameBytes          = 4 * 1024 * 1024
	idleTimeout            = 15 * time.Minute
	kernelMemoryLimitBytes = 1024 * 1024 * 1024
)

type Runtime struct {
	runner      *localexec.Runner
	memoryBytes int64
	idle        time.Duration
	reapEvery   time.Duration

	mu      sync.Mutex
	kernels map[string]*kernel
	starts  map[string]*sync.Mutex
	closed  bool
	stop    chan struct{}
	done    chan struct{}
}

type kernel struct {
	projectID   string
	workspace   string
	id          string
	fingerprint string
	session     *localexec.Session
	input       *bufio.Writer
	frames      chan frameResult
	protocol    chan error
	stderr      *boundedBuffer

	execute sync.Mutex
	mu      sync.Mutex
	lastUse time.Time
	seq     int
}

type frameResult struct {
	ExecutionID     string                 `json:"execution_id"`
	Status          string                 `json:"status"`
	Stdout          string                 `json:"stdout"`
	Stderr          string                 `json:"stderr"`
	StdoutTruncated bool                   `json:"stdout_truncated"`
	StderrTruncated bool                   `json:"stderr_truncated"`
	Value           any                    `json:"value,omitempty"`
	ValueType       string                 `json:"value_type,omitempty"`
	Table           *pythonenv.KernelTable `json:"table,omitempty"`
	Exception       *pythonenv.KernelError `json:"exception,omitempty"`
	Images          []string               `json:"images"`
}

type boundedBuffer struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
	limit     int
}

type Options struct {
	MemoryLimitBytes int64
	IdleTimeout      time.Duration
	ReapInterval     time.Duration
}

func New(runner *localexec.Runner) *Runtime { return NewWithOptions(runner, Options{}) }

func NewWithOptions(runner *localexec.Runner, options Options) *Runtime {
	memory := options.MemoryLimitBytes
	if memory <= 0 {
		memory = kernelMemoryLimitBytes
	}
	idle := options.IdleTimeout
	if idle <= 0 {
		idle = idleTimeout
	}
	reapEvery := options.ReapInterval
	if reapEvery <= 0 {
		reapEvery = time.Minute
	}
	runtime := &Runtime{runner: runner, memoryBytes: memory, idle: idle, reapEvery: reapEvery, kernels: map[string]*kernel{}, starts: map[string]*sync.Mutex{}, stop: make(chan struct{}), done: make(chan struct{})}
	go runtime.reap()
	return runtime
}

func (r *Runtime) Execute(ctx context.Context, request pythonenv.KernelExecuteRequest, workspacePath string) (pythonenv.KernelResult, error) {
	if r == nil || r.runner == nil {
		return pythonenv.KernelResult{}, fmt.Errorf("Python Kernel runtime is not configured")
	}
	value, err := r.getOrStart(request, workspacePath)
	if err != nil {
		return pythonenv.KernelResult{}, err
	}
	value.execute.Lock()
	defer value.execute.Unlock()
	if value.session == nil {
		return pythonenv.KernelResult{}, fmt.Errorf("Python Kernel is stopped")
	}
	executionID, err := id.New()
	if err != nil {
		return pythonenv.KernelResult{}, err
	}
	value.session.SetRuntimeCallID(request.ToolCallID)
	defer value.session.SetRuntimeCallID("")
	outputRelative := filepath.ToSlash(strings.TrimSpace(request.FigurePath))
	if outputRelative == "" {
		outputRelative = filepath.ToSlash(filepath.Join("analysis-output", "figures", executionID))
	}
	outputRoot := filepath.Join(workspacePath, filepath.FromSlash(outputRelative))
	if err := os.MkdirAll(outputRoot, 0o700); err != nil {
		return pythonenv.KernelResult{}, fmt.Errorf("create Kernel figure directory: %w", err)
	}
	started := time.Now().UTC()
	value.mu.Lock()
	value.seq++
	sequence := value.seq
	value.lastUse = started
	value.mu.Unlock()
	partial := func() pythonenv.KernelResult {
		return pythonenv.KernelResult{KernelID: value.id, ExecutionID: executionID, Sequence: sequence, Status: "failed", Images: []string{}, StartedAt: started, FinishedAt: time.Now().UTC()}
	}
	frame := map[string]any{
		"execution_id": executionID,
		"code":         request.Code,
		"workspace":    workspacePath,
		"inputs":       kernelInputAbsolutePaths(workspacePath, request.InputPaths),
		"input_data":   json.RawMessage(request.InputData),
		"outputs":      kernelInputAbsolutePaths(workspacePath, request.OutputPaths),
		"image_dir":    outputRelative,
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return pythonenv.KernelResult{}, err
	}
	if _, err := value.input.Write(append(encoded, '\n')); err != nil {
		r.stopKernel(value, localexec.ReasonExitNonZero)
		return partial(), fmt.Errorf("write Python Kernel request: %w", err)
	}
	if err := value.input.Flush(); err != nil {
		r.stopKernel(value, localexec.ReasonExitNonZero)
		return partial(), fmt.Errorf("flush Python Kernel request: %w", err)
	}
	timer := time.NewTimer(request.Timeout)
	defer timer.Stop()
	for {
		select {
		case frame := <-value.frames:
			if frame.ExecutionID != executionID {
				r.stopKernel(value, localexec.ReasonExitNonZero)
				return pythonenv.KernelResult{}, fmt.Errorf("Python Kernel protocol returned an unexpected execution identity")
			}
			value.mu.Lock()
			value.lastUse = time.Now().UTC()
			value.mu.Unlock()
			finished := time.Now().UTC()
			result := pythonenv.KernelResult{
				KernelID: value.id, ExecutionID: executionID, Sequence: sequence, Status: frame.Status,
				Stdout: frame.Stdout, Stderr: frame.Stderr, StdoutTruncated: frame.StdoutTruncated, StderrTruncated: frame.StderrTruncated,
				Value: frame.Value, ValueType: frame.ValueType, Table: frame.Table, Exception: frame.Exception,
				Images: nonNil(frame.Images), StartedAt: started, FinishedAt: finished,
			}
			if frame.StdoutTruncated || frame.StderrTruncated {
				r.stopKernel(value, localexec.ReasonExitNonZero)
				return result, fmt.Errorf("Python Kernel output exceeded the 64 KiB per-stream limit; the Kernel was stopped")
			}
			if frame.Status == "error" {
				if frame.Exception != nil && frame.Exception.Type == "MemoryError" {
					r.stopKernel(value, localexec.ReasonExitNonZero)
				}
				return result, nil
			}
			if frame.Status != "success" {
				r.stopKernel(value, localexec.ReasonExitNonZero)
				return result, fmt.Errorf("Python Kernel returned invalid status %q", frame.Status)
			}
			return result, nil
		case protocolErr := <-value.protocol:
			r.stopKernel(value, localexec.ReasonExitNonZero)
			return partial(), fmt.Errorf("Python Kernel protocol failed: %w; native stderr: %s", protocolErr, value.stderr.String())
		case <-value.session.Done():
			outcome := value.session.Wait()
			r.removeKernel(value)
			return partial(), fmt.Errorf("Python Kernel exited under its %d MiB process-tree memory budget (%s, code %d): %s", r.memoryBytes/(1024*1024), outcome.Reason, outcome.ExitCode, value.stderr.String())
		case <-timer.C:
			r.stopKernel(value, localexec.ReasonTimedOut)
			return partial(), context.DeadlineExceeded
		case <-ctx.Done():
			reason := localexec.ReasonCancelled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = localexec.ReasonTimedOut
			}
			r.stopKernel(value, reason)
			return partial(), ctx.Err()
		}
	}
}

func kernelInputAbsolutePaths(workspace string, paths []string) []string {
	result := make([]string, len(paths))
	for index, value := range paths {
		clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(value)))
		// Python accepts forward-slash absolute paths on Windows, avoiding
		// ambiguity when the path is transported through JSON and a persistent
		// interpreter session.
		absolute := filepath.Clean(filepath.Join(workspace, clean))
		if runtime.GOOS == "windows" && !strings.HasPrefix(absolute, `\\?\`) {
			// The private task root plus a content-addressed filename can exceed
			// MAX_PATH. Python's Win32 file APIs accept the extended-length form.
			absolute = `\\?\` + absolute
		}
		result[index] = absolute
	}
	return result
}

func kernelEnvironment() ([]string, []string) {
	// Native numeric libraries may otherwise create one worker per logical CPU.
	// On Windows, their thread-local buffers count against the Kernel Job Object's
	// process-tree budget and can make even a small NumPy/SciPy import fail.
	return localexec.CoreEnvironment(map[string]string{
		"PYTHONIOENCODING":       "utf-8",
		"PYTHONUTF8":             "1",
		"PYTHONUNBUFFERED":       "1",
		"MPLBACKEND":             "Agg",
		"NO_COLOR":               "1",
		"OPENBLAS_NUM_THREADS":   "1",
		"OMP_NUM_THREADS":        "1",
		"MKL_NUM_THREADS":        "1",
		"BLIS_NUM_THREADS":       "1",
		"NUMEXPR_NUM_THREADS":    "1",
		"VECLIB_MAXIMUM_THREADS": "1",
	})
}

func (r *Runtime) getOrStart(request pythonenv.KernelExecuteRequest, workspacePath string) (*kernel, error) {
	key := kernelKey(request.ProjectID, workspacePath)
	start := r.projectStartLock(key)
	start.Lock()
	defer start.Unlock()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("Python Kernel runtime is closed")
	}
	value := r.kernels[key]
	if value != nil && value.fingerprint == request.Environment.EnvironmentFingerprint {
		r.mu.Unlock()
		return value, nil
	}
	if value != nil {
		delete(r.kernels, key)
	}
	r.mu.Unlock()
	if value != nil {
		value.execute.Lock()
		_ = value.session.Close()
		value.execute.Unlock()
	}
	kernelID, err := id.New()
	if err != nil {
		return nil, err
	}
	environment, _ := kernelEnvironment()
	session, err := r.runner.StartSession(localexec.SessionRequest{
		Program:          request.Environment.EnvironmentPythonPath,
		Args:             []string{"-I", "-u", "-X", "utf8", "-c", kernelBootstrap},
		Dir:              workspacePath,
		Env:              environment,
		MemoryLimitBytes: r.memoryBytes,
	})
	if err != nil {
		return nil, err
	}
	value = &kernel{projectID: request.ProjectID, workspace: workspacePath, id: kernelID, fingerprint: request.Environment.EnvironmentFingerprint, session: session,
		input: bufio.NewWriterSize(session.Stdin(), 64*1024), frames: make(chan frameResult, 1), protocol: make(chan error, 1), stderr: &boundedBuffer{limit: 64 * 1024}, lastUse: time.Now().UTC()}
	go value.readFrames()
	go func() { _, _ = io.Copy(value.stderr, session.Stderr()) }()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = session.Close()
		return nil, fmt.Errorf("Python Kernel runtime is closed")
	}
	if existing := r.kernels[key]; existing != nil {
		r.mu.Unlock()
		_ = session.Close()
		return nil, fmt.Errorf("Python Kernel lifecycle changed while starting")
	}
	r.kernels[key] = value
	r.mu.Unlock()
	return value, nil
}

func kernelKey(projectID, workspacePath string) string {
	return strings.TrimSpace(projectID) + "\x00" + filepath.Clean(strings.TrimSpace(workspacePath))
}

func (r *Runtime) projectStartLock(projectID string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock := r.starts[projectID]
	if lock == nil {
		lock = &sync.Mutex{}
		r.starts[projectID] = lock
	}
	return lock
}

func (k *kernel) readFrames() {
	scanner := bufio.NewScanner(k.session.Stdout())
	scanner.Buffer(make([]byte, 64*1024), maxFrameBytes)
	for scanner.Scan() {
		var frame frameResult
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			select {
			case k.protocol <- fmt.Errorf("invalid JSON frame: %w", err):
			default:
			}
			return
		}
		select {
		case k.frames <- frame:
		case <-k.session.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case k.protocol <- err:
		default:
		}
	}
}

func (r *Runtime) Stop(projectID string) error {
	projectID = strings.TrimSpace(projectID)
	start := r.projectStartLock(projectID)
	start.Lock()
	defer start.Unlock()
	r.mu.Lock()
	values := make([]*kernel, 0)
	for key, value := range r.kernels {
		if value.projectID == projectID {
			delete(r.kernels, key)
			values = append(values, value)
		}
	}
	r.mu.Unlock()
	var errs []error
	for _, value := range values {
		value.execute.Lock()
		errs = append(errs, value.session.Close())
		value.execute.Unlock()
	}
	return errors.Join(errs...)
}

// StopScoped terminates only the interpreter attached to one task workspace.
func (r *Runtime) StopScoped(projectID, workspacePath string) error {
	key := kernelKey(projectID, workspacePath)
	start := r.projectStartLock(key)
	start.Lock()
	defer start.Unlock()
	r.mu.Lock()
	value := r.kernels[key]
	if value != nil {
		delete(r.kernels, key)
	}
	r.mu.Unlock()
	if value == nil {
		return nil
	}
	value.execute.Lock()
	defer value.execute.Unlock()
	return value.session.Close()
}

func (r *Runtime) RestartScoped(projectID, workspacePath string) error {
	return r.StopScoped(projectID, workspacePath)
}

func (r *Runtime) Restart(projectID string) error { return r.Stop(projectID) }

func (r *Runtime) stopKernel(value *kernel, reason localexec.TerminationReason) {
	r.removeKernel(value)
	_ = value.session.Terminate(reason)
}

func (r *Runtime) removeKernel(value *kernel) {
	r.mu.Lock()
	key := kernelKey(value.projectID, value.workspace)
	if r.kernels[key] == value {
		delete(r.kernels, key)
	}
	r.mu.Unlock()
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		<-r.done
		return nil
	}
	r.closed = true
	close(r.stop)
	values := make([]*kernel, 0, len(r.kernels))
	for _, value := range r.kernels {
		values = append(values, value)
	}
	r.kernels = map[string]*kernel{}
	r.mu.Unlock()
	var errs []error
	for _, value := range values {
		errs = append(errs, value.session.Close())
	}
	<-r.done
	return errors.Join(errs...)
}

func (r *Runtime) reap() {
	defer close(r.done)
	ticker := time.NewTicker(r.reapEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().UTC().Add(-r.idle)
			r.mu.Lock()
			values := make([]*kernel, 0)
			for id, value := range r.kernels {
				value.mu.Lock()
				last := value.lastUse
				value.mu.Unlock()
				if last.Before(cutoff) {
					delete(r.kernels, id)
					values = append(values, value)
				}
			}
			r.mu.Unlock()
			for _, value := range values {
				_ = value.session.Close()
			}
		case <-r.stop:
			return
		}
	}
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		b.data = append(b.data, value[:min(len(value), remaining)]...)
	}
	if len(value) > remaining {
		b.truncated = true
	}
	return len(value), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.ToValidUTF8(string(b.data), "\uFFFD")
}
func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

const kernelBootstrap = `import ast as _ast, contextlib as _contextlib, io as _io, json as _json, os as _os, sys as _sys, traceback as _traceback

class _BoundedIO(_io.TextIOBase):
    def __init__(self, limit=65536):
        self.parts, self.size, self.limit, self.truncated = [], 0, limit, False
    def write(self, value):
        value = str(value); encoded = value.encode("utf-8", "replace"); remaining = self.limit - self.size
        if remaining > 0:
            kept = encoded[:remaining].decode("utf-8", "ignore"); self.parts.append(kept); self.size += len(kept.encode("utf-8"))
        if len(encoded) > remaining: self.truncated = True
        return len(value)
    def getvalue(self): return "".join(self.parts)

def _cell(value):
    if value is None or isinstance(value, (bool, int, float)): return value
    text = str(value); return text if len(text) <= 500 else text[:500] + "..."

def _project(value):
    table = None
    try:
        if hasattr(value, "columns") and hasattr(value, "iloc"):
            cols = [str(x)[:200] for x in list(value.columns)[:50]]; total = int(len(value)); rows = []
            for row in value.iloc[:100, :50].itertuples(index=False, name=None): rows.append([_cell(x) for x in row])
            table = {"columns": cols, "rows": rows, "total": total}
        elif isinstance(value, list) and value and isinstance(value[0], dict):
            cols = [str(x)[:200] for x in list(value[0].keys())[:50]]; rows = [[_cell(row.get(col)) for col in cols] for row in value[:100]]
            table = {"columns": cols, "rows": rows, "total": len(value)}
    except Exception: table = None
    try: projected = _json.loads(_json.dumps(value, ensure_ascii=False))
    except Exception: projected = _cell(repr(value))
    return projected, type(value).__name__, table

_globals = {"__name__": "__main__"}
_control = _sys.stdout
for _line in _sys.stdin:
    try:
        _request = _json.loads(_line); _eid = str(_request["execution_id"]); _code = str(_request["code"])
        _stdout, _stderr = _BoundedIO(), _BoundedIO(); _value = None; _value_type = ""; _table = None; _images = []
        _globals["SCIAIDE_WORKSPACE"] = str(_request["workspace"])
        _globals["SCIAIDE_INPUTS"] = list(_request.get("inputs") or [])
        _globals["SCIAIDE_DATA"] = _request.get("input_data")
        _globals["SCIAIDE_OUTPUTS"] = list(_request.get("outputs") or [])
        _os.chdir(_globals["SCIAIDE_WORKSPACE"])
        try:
            with _contextlib.redirect_stdout(_stdout), _contextlib.redirect_stderr(_stderr):
                _tree = _ast.parse(_code, filename="<sciaide-kernel>", mode="exec")
                _tail = _tree.body[-1] if _tree.body else None
                _publish_result = (
                    isinstance(_tail, _ast.Assign) and len(_tail.targets) == 1
                    and isinstance(_tail.targets[0], _ast.Name) and _tail.targets[0].id == "result"
                ) or (
                    isinstance(_tail, _ast.AnnAssign) and _tail.value is not None
                    and isinstance(_tail.target, _ast.Name) and _tail.target.id == "result"
                )
                if _tree.body and isinstance(_tree.body[-1], _ast.Expr):
                    _last = _ast.Expression(_tree.body.pop().value); exec(compile(_tree, "<sciaide-kernel>", "exec"), _globals); _value = eval(compile(_last, "<sciaide-kernel>", "eval"), _globals)
                else:
                    exec(compile(_tree, "<sciaide-kernel>", "exec"), _globals)
                    # Only this execution's terminal assignment explicitly
                    # publishes result. Never fall back to a persistent global
                    # after print(), unrelated assignments, or empty code.
                    if _publish_result: _value = _globals["result"]
                if _value is not None: _value, _value_type, _table = _project(_value)
                try:
                    import matplotlib.pyplot as _plt
                    for _num in list(_plt.get_fignums())[:16]:
                        _path = _os.path.join(str(_request["image_dir"]), "figure-%02d.png" % _num); _plt.figure(_num).savefig(_path, dpi=144, bbox_inches="tight"); _images.append(_path)
                    _plt.close("all")
                except ImportError: pass
            _response = {"execution_id":_eid,"status":"success","stdout":_stdout.getvalue(),"stderr":_stderr.getvalue(),"stdout_truncated":_stdout.truncated,"stderr_truncated":_stderr.truncated,"value":_value,"value_type":_value_type,"table":_table,"images":_images}
        except BaseException as _exc:
            _response = {"execution_id":_eid,"status":"error","stdout":_stdout.getvalue(),"stderr":_stderr.getvalue(),"stdout_truncated":_stdout.truncated,"stderr_truncated":_stderr.truncated,"images":[],"exception":{"type":type(_exc).__name__,"message":str(_exc)[:4000],"traceback":_traceback.format_exc()[-32768:]}}
    except BaseException as _protocol_exc:
        _response = {"execution_id":str(locals().get("_eid","")),"status":"protocol_error","stdout":"","stderr":"","stdout_truncated":False,"stderr_truncated":False,"images":[],"exception":{"type":type(_protocol_exc).__name__,"message":str(_protocol_exc)[:4000],"traceback":_traceback.format_exc()[-32768:]}}
    _control.write(_json.dumps(_response, ensure_ascii=True, separators=(",",":")) + "\n"); _control.flush()
`
