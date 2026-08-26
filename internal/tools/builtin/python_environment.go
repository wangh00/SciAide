package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const PythonEnvironmentInstallName = "builtin.python.environment.install"

type PythonEnvironmentInstall struct{ environments *pythonenv.Service }

func NewPythonEnvironmentInstall(environments *pythonenv.Service) *PythonEnvironmentInstall {
	return &PythonEnvironmentInstall{environments: environments}
}

func (*PythonEnvironmentInstall) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: PythonEnvironmentInstallName,
		Description:   "Install explicitly named PyPI packages into the current project's SciAide-managed Python environment. User-owned external virtual environments are never modified. The existing lock is recreated in staging, requested packages are installed, freeze and fingerprints are verified, then the environment is atomically replaced. URLs, local paths, indexes and arbitrary pip flags are rejected. Network access is allowed by default, but this modifies the project environment and requires the normal high-risk tool approval in Plan mode.",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["packages"],"properties":{"packages":{"type":"array","minItems":1,"maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256,"pattern":"^[A-Za-z0-9][A-Za-z0-9._-]*(?:\\[[A-Za-z0-9._,-]+\\])?(?:(?:==|!=|~=|>=|<=|>|<)[A-Za-z0-9][A-Za-z0-9._+!-]*)?$"}}}}`),
		OutputSchema:  pythonEnvironmentOutputSchema(),
		Risk:          tool.RiskHigh,
		Permissions: []tool.PermissionRequirement{
			{Kind: tool.PermissionProcessExecute, Resource: "python-pip"},
		},
		Idempotent: false,
		Version:    "2",
	}, nil
}

func (t *PythonEnvironmentInstall) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.environments == nil {
		return tool.Result{}, fmt.Errorf("Python environment management is not configured")
	}
	var args struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	value, err := t.environments.Install(ctx, invocation.ProjectID, args.Packages)
	if err != nil {
		return tool.Result{}, err
	}
	structured, err := json.Marshal(value)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("Project Python environment updated: %d locked packages; fingerprint %s.", len(value.Lock), value.EnvironmentFingerprint), Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func pythonEnvironmentOutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["id","projectId","state","environmentKind","baseExecutablePath","baseExecutableVersion","baseExecutableSha256","architecture","implementation","environmentPythonPath","environmentFingerprint","lock","freezeSha256","createdAt","updatedAt"],"properties":{"id":{"type":"string"},"projectId":{"type":"string"},"state":{"type":"string","enum":["ready"]},"environmentKind":{"type":"string","enum":["legacy_managed","workspace_managed","external"]},"baseExecutablePath":{"type":"string"},"baseExecutableVersion":{"type":"string"},"baseExecutableSha256":{"type":"string"},"architecture":{"type":"string"},"implementation":{"type":"string"},"environmentPythonPath":{"type":"string"},"environmentFingerprint":{"type":"string"},"lock":{"type":"array","items":{"type":"string"}},"freezeSha256":{"type":"string"},"createdAt":{"type":"string"},"updatedAt":{"type":"string"}}}`)
}
