import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

test("research mode is a primary workspace mode rather than a utility modal", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /type WorkspaceMode = "chat" \| "research"/);
  assert.match(source, /aria-label="工作模式"/);
  assert.match(source, />自由对话<\/button>/);
  assert.match(source, />科研模式<\/button>/);
  assert.match(source, /<WorkflowStudio key=\{selectedProject\.id\} project=\{selectedProject\}/);
  assert.doesNotMatch(source, /workflowOpen|setWorkflowOpen/);
  assert.doesNotMatch(source, /<WorkflowStudio[^>]*close=/);
});

test("project resources stay in the shared topbar while engineering controls remain advanced", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const topbar = source.match(/<header className="topbar">([\s\S]*?)<\/header>/)?.[1] ?? "";
  assert.match(topbar, /className="research-open"[\s\S]*?<span>文献发现<\/span>/);
  assert.match(topbar, /className="python-open"[\s\S]*?<span>Python 环境<\/span>/);
  assert.match(topbar, /className="artifact-open"[\s\S]*?<span>科研产物<\/span>/);
  assert.match(topbar, /className="knowledge-open"[\s\S]*?<span>知识库<\/span>/);
  assert.doesNotMatch(source, /research-resource-tools/);
  assert.doesNotMatch(source, /function WorkflowStudio\(\{ project, open/);
  assert.match(source, />研究方案<\/button>/);
  assert.match(source, />任务进度<\/button>/);
  assert.match(source, />高级设置<\/button>/);
  assert.match(source, /view === "advanced"[\s\S]*Workflow JSON/);
});

test("research mode has a persistent visual identity and reduced-motion fallback", async () => {
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(css, /\.mode-research \.workspace\s*\{[^}]*background:\s*#f3f5f8/s);
  assert.match(css, /\.research-mode-workspace\s*\{[^}]*animation:\s*research-workspace-enter/s);
  assert.match(css, /\.research-mode-indicator > i\s*\{[^}]*animation:\s*research-status-pulse/s);
  assert.match(css, /\.research-mode-hero\s*\{[^}]*background:\s*#20242d/s);
  assert.doesNotMatch(css, /\.research-mode-hero\s*\{[^}]*background:\s*#24483a/s);
  assert.match(css, /@media \(prefers-reduced-motion:\s*reduce\)/);
});

test("research templates expose a stable selection and explicit activation feedback", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /const \[selectedTemplateId, setSelectedTemplateId\] = useState\(""\)/);
  assert.match(source, /const \[selectedTemplateDefinition, setSelectedTemplateDefinition\] = useState<WorkflowDefinition \| null>\(null\)/);
  assert.match(source, /aria-pressed=\{!selectedId && selectedTemplateId === template\.id\}/);
  assert.match(source, /if \(selectedTemplateDefinition\) return selectedTemplateDefinition/);
  assert.match(source, /选择方案本身不会立即运行/);
  assert.match(source, /pythonOpen && selectedProject && <PythonEnvironmentSettings/);
});

test("research task setup uses guided data controls and explicit preflight instead of raw JSON", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /function WorkflowInputFields/);
  assert.match(source, /选择数据文件/);
  assert.match(source, /这次希望解决什么问题/);
  assert.match(source, /workflowAnalysisMethods/);
  assert.match(source, /ChooseInputFile/);
  assert.match(source, /项目 Python 运行环境已就绪/);
  assert.match(source, /启动后将自动创建项目 Python 运行环境/);
  assert.match(source, /未检测到可用的 Python 3/);
  assert.match(source, /任务输入已就绪/);
  assert.match(source, /输入、方案版本与工具权限会在任务启动时冻结/);
  assert.match(source, /查看科研产物/);
});

test("research workflow selection gates stale async responses and keeps current-version guide semantics", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /const activeWorkflowIdRef = useRef\(""\)/);
  assert.match(source, /const workflowRequestRef = useRef\(0\)/);
  assert.match(source, /const runsRequestRef = useRef\(0\)/);
  assert.match(source, /const runRequestRef = useRef\(0\)/);
  assert.match(source, /const listRequestRef = useRef\(0\)/);
  assert.match(source, /const activeRunIdRef = useRef\(""\)/);
  assert.match(source, /if \(quiet && activeRunIdRef\.current !== runId\) return/);
  assert.match(source, /const mutationRef = useRef\(""\)/);
  assert.match(source, /if \(detail\) return currentDefinition/);
  assert.match(source, /version\.runtimeInputs\?\.length/);
  assert.match(source, /loaded\.run\.workflowId !== workflowId/);
  assert.match(source, /if \(request !== listRequestRef\.current\) return/);
  assert.match(source, /if \(busy\) return/);
  assert.match(source, /if \(port\.fileKind\)/);
  assert.match(source, /port\.control === "analysis_request"/);
});

test("research discovery rejects stale candidate list responses", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /const candidateRequests = useRef\(createLatestRequestGate\(\)\)/);
  assert.match(source, /const request = candidateRequests\.current\.begin\(\)/);
  assert.match(source, /if \(!candidateRequests\.current\.isCurrent\(request\)\) return/);
  assert.match(source, /candidateRequests\.current\.invalidate\(\)/);
});

test("literature discovery and evidence views keep a readable type floor", async () => {
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(css, /\/\* Literature discovery and evidence workspace readability\. \*\//);
  assert.match(css, /\.research-candidate-list b\s*\{[^}]*font-size:\s*12px/s);
  assert.match(css, /\.research-abstract p\s*\{[^}]*font-size:\s*11\.5px/s);
  assert.match(css, /\.bibliography-fields label,[^{]+\{[^}]*font-size:\s*11px/s);
  assert.match(css, /\.evidence-list article > p,[^{]+\{[^}]*font-size:\s*11px/s);
  assert.match(css, /@media \(max-width:\s*1140px\)\s*\{\s*\.research-layout\s*\{[^}]*grid-template-columns/s);
});

test("Python environment selection is cancellation-safe and separates creation from binding", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /status: "available" \| "unavailable" \| "cancelled"/);
  assert.match(source, /if \(detected\?\.status === "cancelled"\) return/);
  assert.match(source, /Array\.isArray\(detected\?\.interpreters\)/);
  assert.match(source, /ChooseBaseInterpreter/);
  assert.match(source, /ChooseExistingEnvironment/);
  assert.match(source, /BindProjectEnvironment/);
  assert.match(source, /使用已有虚拟环境/);
  assert.match(source, /创建项目环境/);
  assert.match(source, /外部虚拟环境/);
  assert.match(source, /解除绑定/);
  assert.match(source, /\.sciaide\\\\python\\\\venv/);
  assert.match(source, /!ready && !broken/);
  assert.match(source, /重建旧版全局环境/);
  assert.match(source, /基础 Python 只负责创建/);
  assert.match(source, /实际项目运行解释器/);
});

test("research runs freeze an explicit permission mode and saved plans are deletable", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /useState<PermissionMode>\("full_access"\)/);
  assert.match(source, /permissionMode: runPermissionMode/);
  assert.match(source, /Full Access · 自动执行/);
  assert.match(source, /Plan · 逐次确认/);
  assert.match(source, /WorkflowFacade", "Delete"/);
  assert.match(source, /删除方案及任务历史/);
});

test("frameless titlebar supports double-click maximize without handling control buttons", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /className="window-titlebar" onDoubleClick=/);
  assert.match(source, /closest\("\.window-controls"\)/);
  assert.match(source, /toggleMaximiseWindow\(\)/);
});
