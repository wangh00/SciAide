package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const transformVersion = "p7.3-v2"

const executionBoundary = `> **SciAide execution boundary:** Default Skill files are embedded read-only context, not an executable directory. Before running a bundled script, use ` + "`builtin.skill.resource.materialize`" + ` to publish the reviewed file into the current Workspace, inspect it, and then invoke ` + "`builtin.python.execute`" + ` or ` + "`builtin.shell.execute`" + ` through the normal Tool approval path. Treat relative commands below as examples rooted at the materialized files; do not assume an upstream ` + "`skills/`" + ` directory exists. Install project dependencies only through ` + "`builtin.python.environment.install`" + ` with separate approval. SciAide never injects model, MCP, or application secrets into child processes.
`

var completeOverrides = map[string]string{
	"other/skill-installer/SKILL.md": `---
name: skill-installer
description: Explain how third-party Skills are installed or removed through SciAide's reviewed local management interface.
category: other
entry: false
---

# Third-party Skill management

SciAide does not expose Skill installation as an agent command. Installation and removal are user-owned management operations in the **Skills** page.

- Git installs are pinned to a commit SHA and pass local path, size, link, and content review.
- Warnings require explicit confirmation for the same reviewed SHA.
- Removal uses a recoverable local archive.
- Installation never grants Tool, MCP, filesystem, process, or secret permissions.

Do not run an upstream CLI, write directly into a Skill store, or claim cloud synchronization. Direct the user to the Skills page and report this host boundary honestly.
`,
	"research/initialize-atlas-graph/SKILL.md": `---
name: initialize-atlas-graph
description: Describe the unavailable upstream Atlas Graph integration without claiming that SciAide can create or synchronize it.
category: research
entry: false
---

# Atlas Graph boundary

SciAide has no Atlas Graph account, remote canvas, login session, billing plan, or cloud project synchronization path. Do not run an upstream CLI, create product-specific metadata files, or claim that a graph was initialized.

Use SciAide's local project, knowledge, bibliography, evidence, Citation, Artifact, and Workflow records for supported provenance. When a user explicitly requires Atlas Graph interoperability, state that the integration is unavailable and keep any proposed mapping conceptual.
`,
	"cloud-compute/modal/SKILL.md": `---
name: modal
description: Disclose that SciAide has no governed Modal dispatch control plane; use only for conceptual interoperability guidance.
category: cloud-compute
entry: false
---

# Modal dispatch boundary

SciAide does not provide the upstream paid job broker, managed Modal credentials, remote approval card, or governed upload/capture channel. Do not call a nonexistent compute tool or claim that a remote job was dispatched.

Conceptual Modal application design is still valid research guidance. Actual execution requires a user-owned Modal installation and credentials exposed through a separately configured MCP/CLI path; that external path keeps its own approval, cost, data-transfer, and result-verification responsibilities.
`,
	"llm-tools/generate-image/SKILL.md": `---
name: generate-image
description: Disclose that SciAide has no native managed image-generation Tool or wallet route.
category: llm-tools
entry: false
---

# Managed image generation boundary

SciAide currently has no conversation Tool for hosted image generation, no managed wallet, and no automatic OpenRouter credential route. Do not call a nonexistent image Tool, pass application secrets to Shell/Python, or claim an image was generated.

The bundled helper remains source material only. A future user-owned image MCP/API integration may expose a real Tool; until then, report this capability as unavailable. Deterministic plots and diagrams produced by approved Python execution are separate capabilities and must not be presented as generative-image output.
`,
	"ml-training/colab-finetuning/SKILL.md": `---
name: colab-finetuning
description: Explain Google Colab fine-tuning requirements without claiming a SciAide WebSocket bridge or remote runtime Tool.
category: ml-training
entry: false
---

# Google Colab fine-tuning boundary

SciAide has no Colab notebook generator, WebSocket bridge, remote Jupyter control Tool, or managed Google credentials. Do not call ` + "`colab_notebook`" + ` / ` + "`colab_connect`" + ` or claim that a Colab runtime is attached.

The included material may be used to design an ordinary user-owned Colab notebook for Unsloth or related libraries. The user must launch and control that notebook in Google Colab, explicitly transfer required files, configure provider credentials outside SciAide, and return outputs for local verification. Keep data disclosure, GPU availability, package versions, checkpoint storage, cost, and reproducibility gaps explicit.
`,
	"ml-training/colab-finetuning/references/bridge-setup.md": `# Colab interoperability boundary

SciAide does not provide the upstream WebSocket bridge, Cloudflare tunnel, notebook generator, remote Jupyter controller, or managed Google credentials described by the original package.

For a user-owned Colab workflow:

1. Create an ordinary notebook whose package versions and input hashes are explicit.
2. Upload only data the user has approved for Google Colab.
3. Run and monitor the notebook in the Google Colab interface.
4. Download code, logs, checkpoints, tables, and figures into the SciAide Workspace.
5. Verify returned files locally and register reusable outputs as Artifacts.

Do not call ` + "`colab_notebook`" + `, ` + "`colab_connect`" + `, or other nonexistent SciAide Tools. A future MCP integration must expose and audit its own real Tool contract before remote control can be claimed.
`,
}

var credentialAuto = regexp.MustCompile(`(?mi)^(?:Credentials are|HuggingFace token is) auto-injected by openscience when connected via the dashboard\.\s*$`)
var credentialLink = regexp.MustCompile(`(?mi)^If not set: connect ([^\r\n]+?) at https://app\.syntheticsciences\.ai[^\r\n]*$`)

type inventoryEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type derivedManifest struct {
	SchemaVersion       int              `json:"schemaVersion"`
	TransformVersion    string           `json:"transformVersion"`
	UpstreamManifestSHA string           `json:"upstreamManifestSha256"`
	SkillCount          int              `json:"skillCount"`
	FileCount           int              `json:"fileCount"`
	Files               []inventoryEntry `json:"files"`
}

func main() {
	root := flag.String("root", "", "Skill tree to transform in place")
	upstreamManifest := flag.String("upstream-manifest", "", "frozen upstream manifest")
	outputManifest := flag.String("output-manifest", "", "derived manifest path")
	flag.Parse()
	if strings.TrimSpace(*root) == "" || strings.TrimSpace(*upstreamManifest) == "" || strings.TrimSpace(*outputManifest) == "" {
		panic("-root, -upstream-manifest and -output-manifest are required")
	}
	if err := transformTree(*root); err != nil {
		panic(err)
	}
	if err := writeManifest(*root, *upstreamManifest, *outputManifest); err != nil {
		panic(err)
	}
}

func transformTree(root string) error {
	for relative, contents := range completeOverrides {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(relative)), []byte(contents), 0o644); err != nil {
			return fmt.Errorf("override %s: %w", relative, err)
		}
	}
	return filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "LICENSE" || rel == "NOTICE" {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		text := string(data)
		original := text
		text = strings.ReplaceAll(text, "Modal credentials are injected via OpenScience. The Modal CLI reads `MODAL_TOKEN_ID` and `MODAL_TOKEN_SECRET` from environment automatically.", "SciAide does not inject Modal credentials into child processes. A user-owned Modal CLI must already be configured outside SciAide; verify its own login state before requesting an approved execution.")
		text = strings.ReplaceAll(text, `After any Modal job completes, report usage:

`+"```typescript"+`
// In the CLI agent, after Modal job finishes
await OpenScience.reportUsage({
    service: "modal",
    model: "meta-llama/Llama-3.1-8B",  // or whatever was used
    tokens: estimatedTokens,
    gpu: "H100",
    duration: durationSeconds,
})
`+"```"+``, `After a Modal job completes, compare the result with Modal's own usage and billing records. SciAide has no provider billing-report API and does not infer charges from process output.`)
		text = strings.ReplaceAll(text, "If connected via the Synthetic Sciences dashboard, `TENSORPOOL_KEY` is injected automatically.", "SciAide does not inject `TENSORPOOL_KEY`. Configure the user-owned TensorPool CLI outside SciAide before requesting an approved execution, and never paste the key into chat.")
		text = strings.ReplaceAll(text, "If connected via the Synthetic Sciences dashboard, `PRIME_API_KEY` is injected automatically.", "SciAide does not inject `PRIME_API_KEY`. Configure the user-owned Prime Intellect CLI outside SciAide before requesting an approved execution, and never paste the key into chat.")
		text = strings.ReplaceAll(text, "9. **Report usage**: After completion, report via `OpenScience.reportUsage()` with `service=\"primeintellect\"`", "9. **Verify cost**: Compare the completed run with the provider's own usage and billing records; SciAide has no billing-report API")
		text = strings.ReplaceAll(text, "12. Report usage to OpenScience", "12. Record the provider-reported usage and cost in the local result when available")
		text = strings.ReplaceAll(text, "# --- Exact usage reporting (auto-captured by CLI) ---", "# --- Local usage estimate for user review (not captured by SciAide) ---")
		text = strings.ReplaceAll(text, "### Automatic Usage Reporting (Ground Truth)", "### Local Usage Estimate")
		text = strings.ReplaceAll(text, "**CRITICAL**: All training scripts MUST print a `[OPENSCIENCE_USAGE]` line at the end. The CLI automatically captures this and reports exact billing to the dashboard.", "Optionally print a `[LOCAL_USAGE_ESTIMATE]` line so the user can compare the estimate with provider billing. SciAide does not parse this marker or report usage.")
		text = strings.ReplaceAll(text, "The CLI bash tool scans output for `[OPENSCIENCE_USAGE]` markers and auto-reports to the dashboard — no manual reporting needed.", "SciAide does not scan output markers or report billing. Compare any local estimate with the provider's own usage records.")
		text = strings.ReplaceAll(text, "[OPENSCIENCE_USAGE]", "[LOCAL_USAGE_ESTIMATE]")
		text = credentialAuto.ReplaceAllString(text, "SciAide does not inject application or MCP credentials into Shell/Python. Use a user-owned provider login or a separately configured MCP/API Tool; never ask the user to paste a secret into chat.")
		text = credentialLink.ReplaceAllString(text, "If the $1 credential is missing, configure the user-owned provider client or a dedicated MCP/API Tool. SciAide cannot supply it from the model configuration.")
		text = strings.ReplaceAll(text, "[OPENSCIENCE_USAGE]", "[PROVIDER_USAGE]")
		text = strings.ReplaceAll(text, "OpenScience.reportUsage()", "the provider-native usage API or a local audited result")
		text = strings.ReplaceAll(text, "OpenScience.reportUsage(", "providerClient.reportUsage(")
		text = strings.ReplaceAll(text, "SciAide.reportUsage(", "providerClient.reportUsage(")
		text = strings.ReplaceAll(text, "https://app.syntheticsciences.ai", "the user-owned provider console")
		text = strings.ReplaceAll(text, "OPENSCIENCE_SKILLS_DIR", "SCIAIDE_SKILL_RESOURCE_PATH")
		text = strings.ReplaceAll(text, ".openscience", ".sciaide")
		text = strings.ReplaceAll(text, "OpenScience", "SciAide")
		text = strings.ReplaceAll(text, "openscience", "SciAide")
		text = strings.ReplaceAll(text, "The CLI bash tool scans output for `[PROVIDER_USAGE]` markers and auto-reports to the dashboard — no manual reporting needed.", "SciAide does not parse billing markers or report usage. Verify cost and token use through the provider's own records.")
		text = strings.ReplaceAll(text, "All training scripts MUST print a `[PROVIDER_USAGE]` line at the end. The CLI automatically captures this and reports exact billing to the dashboard.", "Record provider-reported token use and cost in the result when available; SciAide does not perform provider billing reporting.")
		text = strings.ReplaceAll(text, "auto-injected by SciAide", "configured in the user-owned provider client")
		text = strings.ReplaceAll(text, "synced automatically via SciAide dashboard", "configured in the user-owned provider client")
		text = strings.ReplaceAll(text, "check SciAide credential sync", "check the user-owned provider client")
		text = strings.ReplaceAll(text, "connect Tinker in the SciAide dashboard to sync your API key", "configure TINKER_API_KEY in a user-owned provider client; SciAide will not inject it into child processes")
		text = strings.ReplaceAll(text, "connect via SciAide dashboard or export manually", "configure the user-owned provider client; SciAide will not inject credentials into child processes")
		text = strings.ReplaceAll(text, "Inside SciAide, call the native generate_image tool to use a funded wallet instead.", "SciAide has no native managed image-generation Tool or wallet route.")
		text = strings.ReplaceAll(text, "Inside SciAide, call the native generate_image "+"\"\n                \"tool to use a funded wallet instead.", "SciAide has no native managed image-generation Tool or wallet route.")
		text = strings.ReplaceAll(text, "Inside SciAide, call the native `generate_image` tool", "SciAide has no native managed `generate_image` Tool")
		text = strings.ReplaceAll(text, "connected OpenRouter BYOK or a funded\n> SciAide wallet in managed mode", "a user-owned image provider configured outside SciAide")
		text = strings.ReplaceAll(text, "connected OpenRouter BYOK or, when\n> managed spend is enabled, a funded SciAide wallet", "a user-owned image provider configured outside SciAide")
		text = strings.ReplaceAll(text, "when that route is active and otherwise uses a funded wallet in managed mode", "only when a user-owned provider route has been explicitly configured")
		text = strings.ReplaceAll(text, "Use this to run any Unsloth workflow on a Google Colab GPU directly from SciAide — no local GPU required.\n\n### Setup\n1. Generate the bridge notebook: `colab_notebook workflow=bridge`\n2. Upload to Google Colab, select GPU runtime, run all cells\n3. Copy the WebSocket URL → `colab_connect connection_url=\"wss://...\"`", "SciAide cannot attach to Google Colab. Build and run a user-owned notebook in the Colab interface, then return code, logs, checkpoints, tables, and figures to the Workspace for local verification.")
		text = strings.ReplaceAll(text, `> Inside OpenScience, call the native `+"`generate_image`"+` tool for every Nano Banana
> generation or edit. It automatically uses connected OpenRouter BYOK or, when
> managed spend is enabled, a funded OpenScience wallet. Do not run the Python
> wrapper through Bash in-product, and do not silently replace a requested Nano
> Banana figure with matplotlib, TikZ, or Mermaid.`, `> SciAide has no native managed `+"`generate_image`"+` Tool or wallet route. This Skill's bundled
> Python helper is source material for a user-owned image provider: materialize and
> inspect it, then run it only through an approved Python/Shell Tool after configuring
> provider credentials outside SciAide. Do not claim generation without a real provider
> result, and do not silently replace requested AI artwork with a different technique.`)
		text = strings.ReplaceAll(text, "Create any scientific diagram by calling `generate_image` with a publication-specific prompt and output path. Example:", "The following JSON is conceptual input for a separately configured image provider, not a built-in SciAide Tool call. Invoke it only through a real user-configured MCP/API Tool, or adapt the reviewed materialized helper to that provider:")
		text = strings.ReplaceAll(text, `No shell credential setup is required. The native tool selects connected BYOK first
when that route is active and otherwise uses a funded wallet in managed mode. Only
surface a connection or balance error returned by that tool; never request a key in chat.`, `SciAide does not inject image-provider credentials or select a funded route. Configure
a user-owned provider outside SciAide. Use only a real registered MCP/API Tool or an
approved, reviewed materialized helper, and never request a key in chat.`)
		text = strings.ReplaceAll(text, `> This skill uses Nano Banana Pro AI for slide image generation — an external
> model. Inside OpenScience, make every generation/edit with the native
> `+"`generate_image`"+` tool so the request uses connected OpenRouter BYOK or a funded
> OpenScience wallet in managed mode. The Python scripts documented below are
> standalone BYOK helpers, not the in-product wallet route.
> third-party service that may be unavailable or require separate credentials.`, `> This Skill describes an external image provider. SciAide has no native managed
> `+"`generate_image`"+` Tool, wallet, or credential route. Use only a real user-configured
> MCP/API Tool, or materialize and inspect the bundled helper before approved execution.
> The provider may be unavailable, charge money, or require credentials configured
> outside SciAide.`)
		text = strings.ReplaceAll(text, `When creating or replacing a technical figure, first load the `+"`scientific-schematics`"+` skill and call
the native `+"`generate_image`"+` tool for its Nano Banana Pro generation. Do not claim AI figure generation
after drawing a substitute with a generic plotting or shell tool. Use the `+"`generate-image`"+` skill for
non-technical illustrations; it uses the same native BYOK-or-wallet route.`, `When creating or replacing a technical figure, first load the `+"`scientific-schematics`"+`
Skill. SciAide has no native managed image-generation Tool or wallet; use a real
user-configured MCP/API provider or a reviewed materialized helper. Do not claim AI
generation after drawing a substitute with a plotting or shell Tool.`)
		text = strings.ReplaceAll(text, "Call `generate_image` with a technical prompt and `output_path: \"figures/output.png\"`.", "When a real image provider is configured, pass it a technical prompt and save its verified output below `figures/`.")
		text = strings.ReplaceAll(text, "Call `generate_image` with an illustrative prompt and `output_path: \"figures/output.png\"`.", "When a real image provider is configured, pass it an illustrative prompt and save its verified output below `figures/`.")
		text = strings.ReplaceAll(text, "For questions or improvements, see https://syntheticsciences.ai", "For package provenance, consult the bundled LICENSE and NOTICE files.")
		text = strings.ReplaceAll(text, "https://syntheticsciences.ai", "See the bundled upstream NOTICE for provenance")
		if needsExecutionBoundary(file, text) {
			var boundaryErr error
			text, boundaryErr = addExecutionBoundary(text)
			if boundaryErr != nil {
				return fmt.Errorf("add execution boundary to %s: %w", rel, boundaryErr)
			}
		}
		if err := rejectHostRuntimeResidue(rel, text); err != nil {
			return err
		}
		if text != original {
			if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
				return err
			}
		}
		return nil
	})
}

func needsExecutionBoundary(file, text string) bool {
	if filepath.Base(file) != "SKILL.md" || strings.Contains(text, executionBoundary) {
		return false
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "allowed-tools:") && (strings.Contains(lower, "bash") || strings.Contains(lower, "write") || strings.Contains(lower, "edit")) {
		return true
	}
	if info, err := os.Stat(filepath.Join(filepath.Dir(file), "scripts")); err == nil && info.IsDir() {
		return true
	}
	return strings.Contains(lower, "pip install ") || strings.Contains(lower, "python scripts/") || strings.Contains(lower, "python3 scripts/")
}

func addExecutionBoundary(text string) (string, error) {
	if !strings.HasPrefix(text, "---") {
		return "", fmt.Errorf("SKILL.md has no frontmatter")
	}
	closing := strings.Index(text[3:], "\n---")
	if closing < 0 {
		return "", fmt.Errorf("SKILL.md frontmatter is not closed")
	}
	closing += 3 + len("\n---")
	return text[:closing] + "\n\n" + executionBoundary + text[closing:], nil
}

func rejectHostRuntimeResidue(relative, text string) error {
	lower := strings.ToLower(text)
	for _, forbidden := range []string{
		"credentials are injected via sciaide",
		"connected via the synthetic sciences dashboard",
		"providerclient.reportusage",
		"sciaide.reportusage",
		"[provider_usage]",
		"[openscience_usage]",
		"auto-captured by cli",
		"automatic usage reporting",
		"report usage to sciaide",
		"native `generate_image` tool",
		"native byok-or-wallet route",
		"funded sciaide wallet",
	} {
		if strings.Contains(lower, forbidden) {
			return fmt.Errorf("unresolved upstream host runtime instruction %q in %s", forbidden, relative)
		}
	}
	return nil
}

func writeManifest(root, upstreamManifest, output string) error {
	upstream, err := os.ReadFile(upstreamManifest)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(upstream)
	manifest := derivedManifest{SchemaVersion: 1, TransformVersion: transformVersion, UpstreamManifestSHA: hex.EncodeToString(digest[:]), Files: []inventoryEntry{}}
	err = filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		hash := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, inventoryEntry{Path: rel, Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])})
		if rel == "SKILL.md" || strings.HasSuffix(rel, "/SKILL.md") {
			manifest.SkillCount++
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	manifest.FileCount = len(manifest.Files)
	if manifest.SkillCount != 311 || manifest.FileCount != 1624 {
		return fmt.Errorf("unexpected derived tree: skills=%d files=%d", manifest.SkillCount, manifest.FileCount)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(output, append(encoded, '\n'), 0o644)
}
