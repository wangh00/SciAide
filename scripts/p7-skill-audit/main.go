package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type frontmatter struct {
	Name         string   `yaml:"name"`
	Category     string   `yaml:"category"`
	Dependencies []string `yaml:"dependencies"`
	AllowedTools []string `yaml:"allowed-tools"`
}

type entry struct {
	Name             string   `json:"name"`
	PackageHash      string   `json:"packageHash"`
	Capability       string   `json:"capability"`
	Reason           string   `json:"reason"`
	RequiredTools    []string `json:"requiredTools"`
	PythonPackages   []string `json:"pythonPackages"`
	CLIDependencies  []string `json:"cliDependencies"`
	ExternalServices []string `json:"externalServices"`
	Limitations      []string `json:"limitations"`
}

type manifest struct {
	SchemaVersion int     `json:"schemaVersion"`
	AuditVersion  string  `json:"auditVersion"`
	SkillCount    int     `json:"skillCount"`
	Skills        []entry `json:"skills"`
}

var unavailable = map[string]string{
	"initialize-atlas-graph": "SciAide 不提供上游 Atlas Graph 账户、画布和云端项目路径",
	"skill-installer":        "SciAide 使用 Skills 管理页的受审查 Git 安装流程，不提供上游 CLI 与 Dashboard 同步路径",
	"modal":                  "SciAide 当前没有上游 compute_job 付费调度控制面",
	"generate-image":         "SciAide 当前没有可由对话工具调用的托管图像生成服务",
}

var sessionTools = map[string][]string{
	"compact": {"builtin.session.compact"}, "context": {"builtin.session.context"},
	"status": {"builtin.session.status"}, "stop": {"builtin.session.stop"},
	"checkpoint": {"builtin.workflow.checkpoint"}, "goal": {"builtin.workflow.goal"},
	"handoff": {"builtin.workflow.handoff"}, "plan": {"builtin.workflow.plan"},
	"resume": {"builtin.workflow.resume"}, "review": {"builtin.workflow.review"},
	"reproduce": {"builtin.workflow.reproduce"}, "research-workflows": {"builtin.workflow.run"},
	"sources": {"builtin.workflow.sources"}, "verify": {"builtin.workflow.verify"},
	"compare": {"builtin.workflow.compare"}, "export": {"builtin.workflow.export"},
}

var knownPythonPackages = map[string][]string{
	"liteparse":       {"liteparse"},
	"scikit-survival": {"scikit-survival"},
	"sympy":           {"sympy"},
	"vaex":            {"vaex"},
}

var installSpec = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?:\[[A-Za-z0-9._,-]+\])?(?:(?:==|!=|~=|>=|<=|>|<)[A-Za-z0-9][A-Za-z0-9._+!-]*)?$`)

func main() {
	root := filepath.Join("internal", "opensciskill", "defaultskills")
	output := filepath.Join("internal", "opensciskill", "capability-audit.v1.json")
	for index := 1; index < len(os.Args); index++ {
		switch os.Args[index] {
		case "-root":
			index++
			if index >= len(os.Args) {
				panic("-root requires a value")
			}
			root = os.Args[index]
		case "-output":
			index++
			if index >= len(os.Args) {
				panic("-output requires a value")
			}
			output = os.Args[index]
		default:
			panic("unknown argument: " + os.Args[index])
		}
	}
	var values []entry
	err := filepath.WalkDir(root, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		parts := bytes.SplitN(data, []byte("---"), 3)
		if len(parts) != 3 {
			return fmt.Errorf("bad frontmatter: %s", file)
		}
		var meta frontmatter
		if err := yaml.Unmarshal(parts[1], &meta); err != nil {
			return err
		}
		packageRoot := filepath.Dir(file)
		hash, scripts, cli, err := packageDigest(packageRoot)
		if err != nil {
			return err
		}
		body := strings.ToLower(string(parts[2]))
		packages := append([]string{}, meta.Dependencies...)
		packages = append(packages, knownPythonPackages[meta.Name]...)
		packages = append(packages, inferredPythonPackages(string(parts[2]))...)
		cli = append(cli, inferredCLI(meta.Name, string(parts[2]))...)
		item := entry{Name: meta.Name, PackageHash: hash, Capability: "native", Reason: "可使用 SciAide 当前对话、知识库、科研检索、引用与产物能力完成主要方法流程", RequiredTools: []string{}, PythonPackages: clean(packages), CLIDependencies: clean(cli), ExternalServices: []string{}, Limitations: []string{}}
		if reason, ok := unavailable[meta.Name]; ok {
			item.Capability, item.Reason = "unavailable", reason
			item.Limitations = []string{"该上游产品能力未在 SciAide 中伪造等价实现"}
		} else if tools, ok := sessionTools[meta.Name]; ok {
			item.Capability, item.RequiredTools = "unavailable", tools
			item.Reason = "需要 SciAide 会话或 Workflow 宿主能力；对应工具尚未注册"
			item.Limitations = []string{"工具注册后将由运行时能力求交集自动恢复"}
		} else {
			external := externalServices(meta, body)
			unknownTools := unknownAllowedTools(meta.AllowedTools)
			if len(unknownTools) > 0 {
				external = append(external, "用户配置的图像生成 MCP/API")
				item.Limitations = append(item.Limitations, "SciAide 未注册托管 "+strings.Join(unknownTools, "、")+" 工具；仅在用户配置真实外部 Tool/API 后可用")
			}
			item.ExternalServices = external
			if len(external) > 0 {
				item.Capability = "requires_external_service"
				item.Reason = "主要流程可执行，但需要用户拥有并配置对应外部服务或凭据"
			} else if len(item.PythonPackages) > 0 || len(scripts) > 0 || len(cli) > 0 || hasExecutionTool(meta.AllowedTools) {
				item.Capability = "requires_dependency"
				item.Reason = "需要先在项目环境安装列出的 Python 包或命令行依赖；安装会单独确认"
			}
			if len(scripts) > 0 {
				item.RequiredTools = append(item.RequiredTools, "builtin.skill.resource.materialize")
				if scripts[".py"] {
					item.RequiredTools = append(item.RequiredTools, "builtin.python.execute")
				}
				if scripts[".sh"] {
					item.RequiredTools = append(item.RequiredTools, "builtin.shell.execute")
				}
			}
			if len(item.PythonPackages) > 0 {
				item.RequiredTools = append(item.RequiredTools, "builtin.python.environment.install", "builtin.python.kernel.execute")
			}
			if len(item.CLIDependencies) > 0 {
				item.RequiredTools = append(item.RequiredTools, "builtin.shell.execute")
			}
			if hasExecutionTool(meta.AllowedTools) {
				item.RequiredTools = append(item.RequiredTools, "builtin.shell.execute", "builtin.python.execute")
			}
			if (len(scripts) > 0 || hasExecutionTool(meta.AllowedTools)) && !strings.Contains(string(parts[2]), "SciAide execution boundary") {
				return fmt.Errorf("execution-oriented Skill lacks SciAide execution boundary: %s", file)
			}
		}
		item.RequiredTools = clean(item.RequiredTools)
		values = append(values, item)
		return nil
	})
	if err != nil {
		panic(err)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	if len(values) != 311 {
		panic(fmt.Sprintf("expected 311 skills, got %d", len(values)))
	}
	out, err := json.MarshalIndent(manifest{1, "p7.3-v2", len(values), values}, "", "  ")
	if err != nil {
		panic(err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(output, out, 0644); err != nil {
		panic(err)
	}
}

func packageDigest(root string) (string, map[string]bool, []string, error) {
	h := sha256.New()
	scripts := map[string]bool{}
	cli := []string{}
	err := filepath.WalkDir(root, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
		if strings.HasPrefix(rel, "scripts/") {
			scripts[strings.ToLower(filepath.Ext(rel))] = true
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), scripts, clean(cli), err
}

func externalServices(meta frontmatter, body string) []string {
	category := strings.ToLower(meta.Category)
	result := []string{}
	if category == "databases" {
		result = append(result, "领域数据库或其公开 API")
	}
	if category == "cloud-compute" {
		result = append(result, "云计算平台账户")
	}
	if strings.HasSuffix(meta.Name, "-integration") {
		result = append(result, "第三方科研平台账户")
	}
	if strings.Contains(body, "api key") || strings.Contains(body, "access token") || strings.Contains(body, "credentials") {
		result = append(result, "第三方 API 凭据")
	}
	return clean(result)
}

func inferredPythonPackages(body string) []string {
	result := []string{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "$"))
		lower := strings.ToLower(line)
		marker := ""
		for _, candidate := range []string{"uv pip install ", "pip3 install ", "pip install ", "python -m pip install "} {
			if index := strings.Index(lower, candidate); index >= 0 {
				marker = line[index+len(candidate):]
				break
			}
		}
		if marker == "" {
			continue
		}
		if index := strings.Index(marker, "#"); index >= 0 {
			marker = marker[:index]
		}
		for _, value := range strings.Fields(marker) {
			value = strings.Trim(value, "'\"`,\\.;:()")
			if value == "" || strings.HasPrefix(value, "-") || strings.Contains(value, "://") || strings.HasPrefix(value, "git+") || strings.Contains(value, "$") || strings.ContainsAny(value, "{}") || !installSpec.MatchString(value) {
				continue
			}
			switch strings.ToLower(value) {
			case "and", "or", "for", "with", "the", "to", "not", "required", "recommended":
				continue
			}
			result = append(result, value)
		}
	}
	return clean(result)
}

func inferredCLI(name, body string) []string {
	lower := strings.ToLower(body)
	known := map[string][]string{
		"matlab":             {"MATLAB 或 GNU Octave"},
		"cobrapy":            {"GLPK 或其他线性规划求解器"},
		"latex-posters":      {"TeX 发行版"},
		"venue-templates":    {"TeX 发行版"},
		"scientific-writing": {"Pandoc/LaTeX（按输出格式）"},
		"pptx-posters":       {"LibreOffice（可选渲染）"},
	}
	result := append([]string{}, known[name]...)
	for _, candidate := range []struct{ marker, label string }{{"ffmpeg", "FFmpeg"}, {"docker ", "Docker"}, {"apptainer ", "Apptainer"}, {"singularity ", "Singularity"}} {
		if strings.Contains(lower, candidate.marker) {
			result = append(result, candidate.label)
		}
	}
	return clean(result)
}

func hasExecutionTool(values []string) bool {
	for _, v := range values {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "bash", "write", "edit":
			return true
		}
	}
	return false
}

func unknownAllowedTools(values []string) []string {
	result := []string{}
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "read", "write", "edit", "bash":
		default:
			result = append(result, strings.TrimSpace(value))
		}
	}
	return clean(result)
}

func clean(values []string) []string {
	seen := map[string]string{}
	for _, v := range values {
		v = strings.Trim(strings.TrimSpace(v), "\"")
		if v != "" {
			seen[strings.ToLower(v)] = v
		}
	}
	out := make([]string, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}
