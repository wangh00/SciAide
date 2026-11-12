import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

test("research mode is a primary workspace mode rather than a utility modal", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /type WorkspaceMode = "chat" \| "research"/);
  assert.match(source, /aria-label="工作模式"/);
  assert.match(source, />自由对话<\/button>/);
  assert.match(source, />科研模式<\/button>/);
  assert.match(source, /<WorkflowStudio\s+key=\{selectedProject\.id\}\s+project=\{selectedProject\}/);
  assert.doesNotMatch(source, /<WorkflowStudio\s+key=\{`\$\{selectedProject\.id\}:\$\{researchConversationId\}`\}/);
  assert.doesNotMatch(source, /workflowOpen|setWorkflowOpen/);
  assert.doesNotMatch(source, /<WorkflowStudio[^>]*close=/);
});

test("reasoning strength is selectable before a conversation and carried into new work", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /workspaceReasoningLevel/);
  assert.match(source, /reasoningLevel: workspaceReasoningLevel/);
  assert.match(source, /reasoningLevel: opening \? workspaceReasoningLevel : selectedConversation\?\.reasoningLevel \?\? workspaceReasoningLevel/);
  assert.match(source, /value=\{effectiveReasoningLevel\} disabled=\{!selectableModels\.length \|\| busy \|\| researchConversationLocked\}/);
  assert.doesNotMatch(source, /value=\{selectedConversation\?\.reasoningLevel \?\? "medium"\} disabled=\{!selectedConversation \|\| busy\}/);
});

test("project resources stay in the shared topbar while engineering controls remain advanced", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const topbar = source.match(/<header className="topbar">([\s\S]*?)<\/header>/)?.[1] ?? "";
  assert.match(topbar, /className="research-open"[\s\S]*?<span>文献发现<\/span>/);
  assert.match(topbar, /className="python-open"[\s\S]*?<span>Python 环境<\/span>/);
  assert.match(topbar, /className="artifact-open"[\s\S]*?<span>科研产物<\/span>/);
  assert.match(topbar, /className="knowledge-open"[\s\S]*?<span>研究资料库<\/span>/);
  assert.doesNotMatch(source, /research-resource-tools/);
  assert.doesNotMatch(source, /function WorkflowStudio\(\{ project, open/);
  assert.match(source, />创建任务<\/button>/);
  assert.doesNotMatch(source, />任务进度<\/button>/);
  assert.match(source, />编辑方案<\/button>/);
  assert.match(source, /view === "advanced"[\s\S]*Workflow JSON/);
});

test("research mode has a persistent visual identity and reduced-motion fallback", async () => {
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(css, /\.mode-research \.workspace\s*\{[^}]*background:\s*#fff/s);
  assert.match(css, /\.research-mode-workspace\s*\{[^}]*animation:\s*research-workspace-enter/s);
  assert.doesNotMatch(css, /\.research-mode-indicator(?![\w-])/);
  assert.match(css, /\.research-mode-hero\s*\{[^}]*background:\s*#f4f6f5/s);
  assert.match(css, /\.research-mode-hero::before\s*\{[^}]*background:\s*#18877e/s);
  assert.match(css, /\.research-mode-mark\s*\{[^}]*background:\s*#273033/s);
  assert.match(css, /\.research-mode-mark span,[^{]*\.research-mode-mark i\s*\{[^}]*animation:\s*research-mark-orbit/s);
  assert.match(css, /\.research-scene-curtain i\s*\{[^}]*background:\s*#18877e/s);
  assert.match(css, /\.mode-research \.workspace-mode-switch button\.selected\s*\{[^}]*color:\s*#176f69[^}]*box-shadow:[^}]*#18877e/s);
  assert.doesNotMatch(css, /\.research-mode-hero\s*\{[^}]*background:\s*#(?:20242d|24483a|3152bf|4266ed|4f72f6)/s);
  assert.match(css, /@media \(prefers-reduced-motion:\s*reduce\)/);
});

test("research entry shares draft and send controls without adding a bottom chat box", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.equal((source.match(/<form className="composer"/g) ?? []).length, 1);
  assert.equal((source.match(/id="workspace-message-input"/g) ?? []).length, 1);
  assert.match(source, /aria-label=\{researchEntry \? "研究问题" : "聊天消息"\}/);
  assert.match(source, /researchEntry \? createPortal\([\s\S]*researchEntryTarget!\) : <footer className="composer-wrap">\{composerUI\}/);
  assert.match(source, /className="research-entry-slot" ref=\{entrySlotRef\}/);
  assert.match(css, /\.workspace:has\(\.research-entry-slot\)\s*\{[^}]*grid-template-areas:\s*"top top"\s*"hero hero"\s*"home-side home-content";/s);
  assert.match(css, /\.workspace:has\(\.research-mode-workspace\)\s*\{[^}]*grid-template-areas:\s*"top top"\s*"hero hero"\s*"home-side home-content"\s*"home-side compose"/s);
  assert.match(css, /\.research-mode-workspace > \.workflow-layout\s*\{\s*display:\s*contents/s);
  assert.match(css, /\.research-mode-workspace > \.workflow-layout > \.workflow-side\s*\{[^}]*grid-area:\s*home-side/s);
  assert.match(css, /\.research-mode-workspace > \.workflow-layout > main\s*\{[^}]*grid-area:\s*home-content[^}]*overflow:\s*auto/s);
  assert.match(css, /\.workspace:has\(\.research-mode-workspace\) > \.composer-wrap\s*\{[^}]*grid-area:\s*compose/s);
  assert.match(css, /\.research-mode-workspace > \.research-scene-curtain\s*\{[^}]*inset:\s*calc\(64px \+ var\(--research-home-hero-height\)\)/s);
});

test("desktop prompts use the animated in-app dialog system", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.doesNotMatch(source, /window\.(?:alert|confirm|prompt)\s*\(/);
  assert.match(source, /const appDialogQueue: AppDialogRequest\[\] = \[\]/);
  assert.match(source, /function AppDialogHost\(\)/);
  assert.match(source, /return createPortal\(<ModalDialog key=\{request.id\} className=\{`app-dialog-backdrop/);
  assert.match(source, /<\/ModalDialog>, document\.body\);/);
  assert.match(source, /role="dialog" aria-modal="true"/);
  assert.match(source, /className=\{`app-dialog \$\{request\.tone === "danger"/);
  assert.match(await readFile(new URL('./Modal.tsx', import.meta.url), 'utf8'), /querySelectorAll<HTMLElement>/);
  assert.match(css, /\.app-dialog\s*\{[^}]*width:\s*min\(440px/s);
  assert.match(css, /\.app-dialog\s*\{[^}]*max-height:\s*calc\(100vh - 40px\)/s);
  assert.match(css, /\.app-dialog p\s*\{[^}]*overflow-y:\s*auto/s);
  assert.match(css, /animation:\s*app-dialog-in/);
  assert.match(css, /to\s*\{\s*opacity:\s*1;\s*transform:\s*none;\s*\}\s*\}\s*@keyframes app-dialog-out/s);
  const dialogRule = css.match(/\.app-dialog\s*\{([^}]*)\}/s)?.[1] ?? "";
  assert.doesNotMatch(dialogRule, /will-change|backface-visibility|scale\(/);
  assert.match(css, /\.app-dialog-backdrop\.closing/);
  assert.match(css, /\.app-dialog-backdrop\s*\{[^}]*z-index:\s*2000/s);
  assert.match(css, /@media \(prefers-reduced-motion:\s*reduce\)/);
});

test("research start removes template entry and opens past plans only on demand", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(source, /const \[historyPlansOpen, setHistoryPlansOpen\] = useState\(false\)/);
  assert.match(source, /historyPlansOpen && <ResearchPlanHistory/);
  assert.match(source, /plans=\{visibleWorkflows\}/);
  assert.match(source, /openPlan=\{selectWorkflow\} deletePlan=\{deleteWorkflow\} deleteSelected=\{deleteSelectedPlans\}/);
  assert.doesNotMatch(source, /WorkflowTemplateDialog|selectedTemplateDefinition|selectedTemplateId|newFromTemplate|"WorkflowFacade", "Templates"/);
  assert.doesNotMatch(source, /if \(!editorTouched\.current && !selectedId && loaded\[0\]\)/);
  assert.match(css, /\.research-plan-history-body\s*\{[^}]*overflow-y:\s*auto/s);
  assert.match(source, /pythonOpen && selectedProject && <PythonEnvironmentSettings/);
  assert.match(source, /useState<"" \| "adopting" \| "adopted">\(""\)/);
  assert.match(source, /className="research-scene-curtain"/);
  assert.match(css, /\.research-mode-workspace\.scene-adopted \.research-start-panel/);
  assert.match(css, /\.research-guide-inputs > :nth-child\(4\)/);
  assert.match(css, /\.research-scene-curtain\s*\{[^}]*pointer-events:\s*auto/s);
  assert.match(css, /\.research-mode-workspace\.scene-adopted \.research-scene-curtain\s*\{[^}]*pointer-events:\s*none/s);
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
  assert.match(source, /这里创建的是一次独立任务；输入、方案版本与工具权限会在启动时冻结/);
  assert.match(source, /正在查看已有方案，尚未启动新任务/);
  assert.match(source, /查看科研产物/);
});

test("research workflow selection gates stale async responses and keeps current-version guide semantics", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /const activeWorkflowIdRef = useRef\(""\)/);
  assert.match(source, /const workflowRequestRef = useRef\(0\)/);
  assert.match(source, /const projectRunsRequestRef = useRef\(0\)/);
  assert.match(source, /const runRequestRef = useRef\(0\)/);
  assert.match(source, /const listRequestRef = useRef\(0\)/);
  assert.match(source, /const activeRunIdRef = useRef\(""\)/);
  assert.match(source, /if \(quiet && activeRunIdRef\.current !== runId\) return/);
  assert.match(source, /const mutationRef = useRef\(""\)/);
  assert.match(source, /if \(detail\) return currentDefinition/);
  assert.match(source, /version\.runtimeInputs\?\.length/);
  assert.match(source, /loaded\.run\.workflowId !== workflowId/);
  assert.match(source, /if \(request !== listRequestRef\.current\) return/);
  assert.match(source, /if \(request !== projectRunsRequestRef\.current\) return/);
  assert.match(source, /if \(busy\) return/);
  assert.match(source, /if \(port\.fileKind\)/);
  assert.match(source, /port\.control === "analysis_request"/);
});

test("research starter accepts semantic route plans and legacy projections", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const parser = source.match(/function researchStarterPlan\([\s\S]*?\n\}/)?.[0] ?? "";
  assert.match(parser, /Array\.isArray\(plan\.selectedSkills\)/);
  assert.match(parser, /candidate\.stagePlans/);
  assert.match(parser, /const semantic = Array\.isArray\(candidate\.stagePlans\)/);
  assert.match(parser, /const legacy = Array\.isArray\(candidate\.stageIds\)/);
  assert.match(parser, /layers: Array\.isArray\(candidate\.layers\) \? candidate\.layers : \[\]/);
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
  assert.match(source, /await backend<void>\("WorkflowFacade", "Delete", project\.id, value\.id\)/);
  assert.doesNotMatch(source, /for \(const item of group\) await backend<void>\("WorkflowFacade", "Delete"/);
  assert.match(await readFile(new URL('./ResearchPlanHistory.tsx', import.meta.url), 'utf8'), /删除方案及已结束任务历史/);
  assert.match(source, /group\.map\(async \(workflow\) => \(\{ workflow, runs: await backend<WorkflowRun\[]>\("WorkflowFacade", "ListRuns", project\.id, workflow\.id, 100\) \}\)\)/);
  assert.match(source, /filter\(\(run\) => !workflowTerminal\(run\.status\)\)/);
  assert.match(source, /必须先进入任务并点击“取消”/);
  assert.match(source, /破坏运行状态、审计记录和重启后的恢复闭环/);
  assert.match(source, /const owner = workflows\.find\(\(item\) => item\.id === firstActiveRun\.workflowId\) \?\? value/);
  assert.match(source, /await selectWorkflow\(owner\)/);
  assert.match(source, /await loadRun\(firstActiveRun\.id\)/);
  assert.match(source, /message\.includes\("未结束的科研任务"\)/);
  assert.match(source, /await appAlert\(\{ title: `暂时无法删除/);
  assert.match(source, /WorkflowFacade", "DeleteRun"/);
  assert.match(source, /删除任务及科研会话/);
  assert.match(source, /workflowTerminal\(run\.status\) && <button/);
});

test("research runs bind the existing chat loop and restore the selected task", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(source, /conversationId\?: string/);
  assert.match(source, /selectResearchConversation/);
  assert.match(source, /initialConversationId=\{researchConversationId\}/);
  assert.match(source, /WorkflowFacade", "ListProjectRuns", project\.id, 200/);
  assert.match(source, /"GetRunByConversation", initialConversationId/);
  assert.match(source, /className="workspace-content"/);
  assert.match(source, /className="research-route-panel"/);
  assert.match(source, /className="research-evidence-panel"/);
  assert.match(source, /所有决定请在中间协作区完成；右侧只展示交付状态、来源与成果/);
  assert.match(source, /researchConversationLocked/);
  assert.match(source, /publishComposerLocked=\{setResearchComposerLocked\}/);
  assert.match(source, /pendingDecisionStep\?\.nodeKind === "agent_stage"/);
  assert.match(source, /const \[researchConversation, setResearchConversation\] = useState<Conversation \| null>\(null\)/);
  assert.match(source, /"ConversationFacade", "GetConversation", nextConversationId/);
  assert.match(source, /onClick=\{enterChatMode\}/);
  assert.match(source, /conversationIdRef\.current === researchConversationIdRef\.current/);
  assert.match(source, /researchConversationProjectIdRef\.current === selectedProject/);
  assert.match(source, /function workflowTaskQuestion\(run: WorkflowRun\)/);
  assert.match(source, /run\.inputs\?\.\[port\.name\]/);
  assert.match(source, /className="research-task-question"/);
  assert.match(source, /title: taskQuestion\.label, message: taskQuestion\.text/);
  assert.match(css, /\.research-task-question p\s*\{[^}]*-webkit-line-clamp:\s*3/s);
  assert.doesNotMatch(source, /className=\{`conversation-row[^\n]*research-bound/);
  assert.match(source, /科研流程正在自主推进，任务结束后可继续提问/);
  assert.match(source, /流程与 AI 正在完成闭环；可在左侧取消任务/);
  assert.match(source, /busy && !activeRunIsWorkflowAI && !researchConversationLocked \? conversationStopControl/);
  assert.match(source, /pendingQuestionKey && conversationStopControl/);
  assert.match(css, /\.workspace:has\(\.research-route-panel\)\s*\{[^}]*grid-template-areas:\s*[^}]*"route chat evidence"[^}]*"route compose evidence"/s);
  assert.match(css, /\.workspace:has\(\.research-route-panel\) > \.workspace-content\s*\{[^}]*display:\s*contents/s);
  assert.match(css, /\.workspace:has\(\.research-route-panel\) > \.composer-wrap\s*\{[^}]*grid-area:\s*compose[^}]*background:\s*#fff/s);
  assert.match(css, /\.workspace:has\(\.research-route-panel\)\s*\{[^}]*background:\s*#fff/s);
  assert.match(css, /\.research-route-panel\s*\{[^}]*background:\s*#f3f5f4/s);
  assert.match(css, /\.research-evidence-panel\s*\{[^}]*background:\s*#fff/s);
  assert.match(css, /\.mode-research \.workflow-side\.tasks\s*\{[^}]*background:\s*#f1f3f2/s);
  assert.match(css, /@media \(max-width:\s*1220px\)[\s\S]*?grid-template-areas:\s*[^}]*"route chat"[^}]*"route compose"/s);
  assert.match(css, /@media \(max-width:\s*880px\)[\s\S]*?grid-template-areas:\s*[^}]*"chat"[^}]*"compose"/s);
  assert.match(css, /\.workspace:has\(\.research-mode-workspace\) > \.composer-wrap\s*\{[^}]*display:\s*block/s);
});

test("pending adopted routes stage files against the durable task identity", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /type PendingAdoptedRoute = \{ starterRunId: string; researchTaskId: string;/);
  assert.match(source, /ChooseInputFileForResearchRoute", project\.id, kind, pending\.starterRunId/);
  assert.match(source, /const adoptedTaskId = adopted\.researchTaskId \|\| adopted\.run\?\.run\.researchTaskId \|\| adopted\.starterRunId/);
});

test("research run workspace separates route, decisions, and verifiable results", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(source, /<ResearchTaskTimeline/);
  assert.match(source, /const interaction = runDetail \? \(<WorkflowInteractionCards/);
  assert.doesNotMatch(source, /research-interaction-stream|interactionHost/);
  assert.match(source, /文献候选/);
  assert.match(source, /证据选择/);
  assert.match(source, /等待授权/);
  assert.match(source, /成果来源链/);
  assert.match(source, /className="workflow-artifact-lineage"/);
  assert.doesNotMatch(source, /密钥由系统凭据库保护|className="local-note"/);
  assert.doesNotMatch(source, /className="research-mode-indicator"/);
  assert.match(source, /aria-label="打开项目研究资料库"/);
  assert.match(source, /aria-label="打开科研产物"/);
  assert.match(source, /复现快照/);
  assert.doesNotMatch(source, /技术审计/);
  assert.doesNotMatch(source, /AI 阶段快照/);
  assert.doesNotMatch(source, /className="research-stage-details"/);
  assert.doesNotMatch(source, /<h3>阶段记录<\/h3>/);
  assert.doesNotMatch(css, /\.workflow-(?:runtime|run-panel|run-inputs|run-history|run-detail|run-summary|runtime-approvals|step-timeline|step-main|step-retry|step-technical)\b/);
  assert.match(await readFile(new URL('./researchCard.css',import.meta.url),'utf8'), /\.research-card\.research-card\s*\{/);
  assert.match(css, /@keyframes research-decision-in/);
});

test("research delivery view does not render a global Workflow activity card", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.doesNotMatch(source, /function workflowLiveAIActivities\(detail: WorkflowRunDetail\)/);
  assert.doesNotMatch(source, /function WorkflowAIActivityCard\(/);
  assert.doesNotMatch(source, /科研执行过程/);
  assert.match(source, /function WorkflowMessageActivityCard\(/);
  assert.match(source, /function workflowMessageActivities\(detail: WorkflowRunDetail/);
});

test("Workflow AI messages keep their own expandable activity history", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /const workflowAIMessage = Boolean\(message\.internal \|\| researchStageTask \|\| workflowActivity\);/);
  assert.match(source, /type Message = .*workflowStatus\?: WorkflowRunStatus/);
  assert.match(source, /workflowAIMessage && workflowActivity && <WorkflowMessageActivityCard/);
  assert.match(source, /workflowActivity=\{message\.runId \? researchActivities\[message\.runId\] : undefined\}/);
  assert.match(source, /workflowMessageActivities\(detailSnapshot/);
});

test("ordinary chat keeps inspectable run history before its answer", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /message\.role === "assistant" && run && !workflowActivity && <RunProcess/);
  assert.match(source, /message\.role === "assistant" && !run && message\.runId && !workflowActivity && <HistoricalRunProcess/);
  assert.match(source, /message\.role === "assistant" && run && !workflowActivity && <RunProcess/);
});

test("Workflow message activity includes AI, Workflow tools, approvals, and durable history", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(source, /function WorkflowMessageActivityCard\(\{ activity: entry, busy, resolveApproval, retryStatus \}/);
  assert.match(source, /const projected = new Map\(\(detail\.aiActivities \?\? \[\]\)\.map/);
  assert.match(source, /for \(const execution of detail\.aiExecutions\)/);
  assert.match(source, /currentAction: execution\.status === "prepared" \? "正在准备 AI 阶段"/);
  assert.match(source, /const calls = \[\.\.\.\(activity\.toolCalls \?\? \[\]\), \.\.\.entry\.workflowTools\]/);
  assert.match(source, /const approvals = \[\.\.\.entry\.approvals, \.\.\.\(activity\.pendingApprovals \?\? \[\]\)\]/);
  assert.match(source, /UnifiedToolActivityCard/);
  assert.match(source, /tool-card-toggle/);
  assert.match(source, /research-message-activity/);
  assert.match(css, /research-message-activity-state\.spinning/);
  assert.match(css, /tool-card-toggle \.tool-icon\.spinning/);
  assert.match(source, /safeToolArguments\(activity\.arguments\)/);
  assert.doesNotMatch(source, /orderedWorkflowActivityEntries|unmatchedWorkflowApprovals|research-ai-workflow-tools/);
});

test("Workflow activity status and identity stay truthful across refreshes", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /const statusText = waiting \? "等待授权"/);
  assert.match(source, /entry.stageLabel/);
  assert.match(source, /本批筛选/);
  assert.match(source, /progress.total - progress.pending/);
  assert.match(source, /<b>\{statusText\} \{formatResearchActivityDuration\(activity\.elapsedSeconds\)\}<\/b>/);
  assert.match(source, /projectedActivity && projectedActivity\.chatRunId === execution\.chatRunId/);
  assert.match(source, /execution\.completedAt \? Date\.parse\(execution\.completedAt\) : Date\.now\(\)/);
});

test("Workflow tools render inline without a fallback panel", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /<WorkflowTimelineToolCard/);
  assert.doesNotMatch(source, /WorkflowSystemActivityCard|workflowSystemActivityGroups|系统工具活动|workflowToolsAssignedToAI/);
});

test("message merge refreshes durable Workflow identity and status", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /&& live\.internal === message\.internal/);
  assert.match(source, /&& live\.workflowStatus === message\.workflowStatus/);
  assert.match(source, /runDetail\?\.run\.status/);
  assert.match(source, /const normalizeDisplayText = \(value: string\) => value[\s\S]*replace\(\/\\\\r/);
});

test("research tasks are first-class entries separate from reusable plans", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(source, /className="workflow-project-tasks"/);
  assert.match(source, /<h2>科研任务<\/h2>/);
  assert.match(source, /点击即可查看当前进度、协作对话和结果/);
  assert.match(source, /async function openProjectRun\(run: WorkflowRun\)/);
  assert.match(source, /className="workflow-side-tabs"/);
  assert.match(source, /科研任务<small>\{sideView === "loading" \? "正在读取…" : `\$\{projectRuns\.length\} 条运行记录`\}<\/small>/);
  assert.match(source, /研究方案<small>提出问题，开始研究<\/small>/);
  assert.doesNotMatch(source, /<h3>方案模板<\/h3>|<h3>我的方案<\/h3>/);
  assert.match(source, /aria-label="历史方案"/);
  assert.match(source, /每一项对应一个研究问题/);
  assert.match(source, /aria-label="研究如何开始"[\s\S]*先聊问题或材料[\s\S]*一起明确执行计划[\s\S]*执行后继续讨论/);
  assert.match(source, /你想研究什么？/);
  assert.doesNotMatch(source, /不确定路线时交给 AI；目标明确时直接选择模板/);
  assert.match(source, /function workflowTaskTitle\(run: WorkflowRun\)/);
  assert.match(source, /title=\{workflowTaskQuestion\(run\)\?\.text\}>\{workflowTaskTitle\(run\)\}/);
  assert.match(source, /确认采用路线后才执行/);
  assert.match(source, /创建并开始任务/);
  assert.doesNotMatch(source, /<h2>方案库<\/h2>|已保存的可复用方案|>任务进度<\/button>/);
  assert.doesNotMatch(source, /view === "runs"|setView\("runs"\)|workflow-history-browser/);
  assert.match(css, /\.workflow-project-tasks\s*\{[^}]*max-height:\s*230px/s);
  assert.match(css, /\.workflow-side-tabs\s*\{/);
  assert.match(css, /\.workflow-side\.tasks \.workflow-project-tasks\s*\{[^}]*max-height:\s*none/s);
});

test("research starter records are not presented as completed formal tasks", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /function workflowRunStatusLabel\(run: WorkflowRun\)/);
  assert.match(source, /run\.workflowPurpose === "research_starter" && run\.status === "completed"/);
  assert.match(source, /return "路线规划已完成"/);
  assert.match(source, /eyebrow="候选执行计划已准备"/);
  assert.doesNotMatch(source, /AI 研究启动已完成/);
  assert.match(source, /researchTaskId\?: string/);
  assert.match(source, /researchStarterRunId\?: string/);
  assert.match(source, /AI 路线规划与后续执行会作为同一任务连续推进/);
  assert.match(source, /const activeProjectRuns = useMemo/);
  assert.match(source, /\{projectRuns\.length\}<\/b><small>全部任务/);
  assert.match(source, /<ResearchRoute detail=\{runDetail\}/);
  const routeSource = await readFile(new URL('./ResearchRoute.tsx', import.meta.url), 'utf8');
  assert.match(routeSource, /<b><span>AI 路线规划<\/span><\/b>/);
});

test("research mode chooses the useful first screen after task discovery", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(source, /type ResearchSideView = "loading" \| "tasks" \| "plans"/);
  assert.match(source, /useState<ResearchSideView>\("loading"\)/);
  assert.match(source, /const sideViewResolvedRef = useRef\(false\)/);
  assert.match(source, /const sideViewTouchedRef = useRef\(false\)/);
  assert.match(source, /const chooseSideView = useCallback/);
  assert.match(source, /researchSideViewSession\.set\(project\.id, value\)/);
  assert.match(source, /remembered === "tasks" && loaded\.length > 0/);
  assert.match(source, /loaded\.length > 0 \? "tasks" : "plans"/);
  assert.match(source, /if \(!sideViewResolvedRef\.current && !sideViewTouchedRef\.current\)/);
  assert.match(source, /正在准备科研模式/);
  assert.match(css, /@keyframes workflow-entry-spin/);
  assert.match(css, /@media \(prefers-reduced-motion:\s*reduce\)[\s\S]*?workflow-entry-loading svg/s);
});

test("saved plans collapse identical definitions while research tasks remain independent", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /currentDefinitionSha256\?: string/);
  assert.match(source, /const visibleWorkflows = useMemo/);
  assert.match(source, /value\.currentDefinitionSha256 \|\| `workflow:\$\{value\.id\}`/);
  assert.match(source, /plans=\{visibleWorkflows\}/);
  const history = await readFile(new URL('./ResearchPlanHistory.tsx', import.meta.url), 'utf8');
  assert.match(history, /plans\.map\(plan =>/);
  assert.match(history, /<h2>历史方案<\/h2>/);
  assert.match(history, /暂无历史方案/);
  assert.match(history, /打开只查看设置，不会启动任务/);
  assert.match(source, /projectRuns\.map\(\(run\) =>/);
});

test("one-sentence research ideas use a persisted AI starter and host-validated route cards", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(source, /className="research-start-welcome"/);
  assert.match(source, /researchEntry \? "开始讨论" : "发送"/);
  assert.match(source, /WorkflowFacade", "StartResearch"/);
  assert.match(source, /请先在顶部选择协作模型/);
  assert.match(source, /function researchStarterPlan\(detail: WorkflowRunDetail\)/);
  assert.match(source, /const visibleMessageText = \(message: Message\)/);
  assert.match(source, /message\.internal && message\.status !== "complete" \? "" : visibleMessageText\(message\)/);
  assert.match(source, /message\.role === "user" && \(message\.internal \|\| Boolean\(researchStageTask\)\)/);
  assert.match(source, /className="research-interaction-card research-route-choice"/);
  assert.match(source, /WorkflowFacade", "AdoptResearchRoute"/);
  assert.match(source, /采用并开始任务/);
	assert.match(source, /采用并补充数据/);
	assert.match(source, /WorkflowFacade", "ChooseInputFileForResearchRoute"/);
	assert.match(source, /pending\.starterRunId/);
  assert.match(source, /setRunDetail\(adopted\.run \?\? null\)/);
  assert.match(source, /chooseSideView\(adopted\.run \? "tasks" : "plans"\)/);
  assert.match(source, /selectConversation\(adopted\.run\.run\.conversationId\)/);
  assert.match(source, /已采用“\$\{loaded\.workflow\.name\}”并开始正式科研任务/);
  assert.match(source, /workflowInputDraft\(current\.definition, adopted\.initialInputs\)/);
  assert.match(source, /workflowTaskTitle\(run\)/);
  assert.match(source, /const researchStarter = runDetail\.run\.workflowPurpose === "research_starter"/);
  assert.match(source, /候选路线已准备/);
  assert.match(source, /!researchStarter && <WorkflowRunEvidence/);
  assert.doesNotMatch(source, /disabled=\{!owner \|\| Boolean\(busy\)\}/);
  assert.match(css, /\.research-start-welcome\s*\{/);
  assert.match(css, /\.research-route-options\s*\{/);
  assert.match(source, /function ResearchRouteDetailDialog/);
  assert.match(source, /查看路线详情/);
  assert.match(source, /className="research-route-card-meta"/);
  assert.match(source, /className="research-route-card-actions"/);
  assert.match(css, /\.research-route-detail-dialog\s*\{/);
  assert.match(css, /\.research-route-card-summary\s*\{/);
  assert.match(css, /\.research-route-options\s*\{[^}]*grid-template-columns:\s*1fr/s);
});

test("legacy clarification and chat share the same inline question card", async () => {
 const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
 const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
 const dialog = await readFile(new URL("./UserInputCard.tsx", import.meta.url), "utf8");
 assert.match(source, /questions=\{clarificationQuestions\}/);
 assert.match(source, /questions=\{userInputRequest.questions\}/);
 assert.doesNotMatch(source, /ResearchClarificationDialog|setClarificationQuestion|按我的选择生成研究路线/);
 assert.match(source, /starterRunId: runDetail.run.id, answers, freeTextAnswers/);
 assert.match(dialog, /<ResearchCard className="user-input-card"/);
 assert.doesNotMatch(dialog, /ModalBackdrop|createPortal|role="dialog"|aria-modal/);
 assert.match(dialog, /className="user-input-free-text"/);
 assert.match(css, /\.user-input-options label\s*\{/);
 assert.doesNotMatch(css, /\.research-clarification-dialog/);
});

test("research route gaps expose user actions instead of host implementation names", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /function researchRouteUserGap\(route: ResearchStarterRoute\): string \| null/);
  assert.match(source, /请上传或选择一份与本课题相关的 CSV、TSV 或 XLSX 数据文件/);
  assert.match(source, /开始前需要：\{userGap\}/);
  assert.doesNotMatch(source, /主要缺口：\{route\.blockers\[0\]\}/);
  assert.doesNotMatch(source, /宿主校验：\{currentRoute\.validationError\}/);
  assert.match(source, /Unknown diagnostics are intentionally hidden/);
  assert.match(source, /当前没有可引用的文献材料，请先检索并选择与本课题相关的文献/);
});

test("all route display text passes through the user-facing terminology filter", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /const researchRouteInternalTermLabels/);
  assert.match(source, /function researchRouteUserText\(value: string \| undefined\): string/);
  assert.match(source, /researchRouteUserText\(route\.title\)/);
  assert.match(source, /researchRouteUserText\(route\.reason\)/);
  assert.match(source, /researchRouteUserText\(currentRoute\.reason\)/);
  assert.match(source, /researchRouteUserText\(layer\.objective\)/);
  assert.match(source, /\(stage\.methods \?\? \[\]\)\.map\(researchRouteUserText\)/);
  assert.match(source, /currentRoute\.requiredResources\.map\(researchRouteUserText\)/);
  assert.match(source, /当前没有可引用的文献材料，请先检索并选择与本课题相关的文献/);
  assert.match(source, /当前项目/);
  assert.match(source, /文献检索来源/);
});

test("workspace reasoning notices are concise and auto-dismiss", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /notice\.startsWith\("已设置下一次对话\/科研任务的思考强度："\)/);
  assert.match(source, /setNotice\(`已设置下一次对话\/科研任务的思考强度：\$\{level\}`\)/);
});

test("empty evidence recovery is explicit and limited to data or design routes", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /allowEmptyCitations/);
  assert.match(source, /无文献证据，继续数据分析/);
  assert.match(source, /未建立外部证据链/);
  assert.match(source, /continueWithoutCitations/);
  assert.match(source, /node\.id === "python_analysis"/);
  assert.match(source, /node\.id === "research_design"/);
	assert.match(source, /decide=\{\(step, approved, continueWithoutCitations, acceptLimited\) => void decide\(step, approved, continueWithoutCitations, acceptLimited\)\}/);
});

test("literature checkpoints default to AI recommendations but preserve explicit user control", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /AI 筛选建议/);
  assert.match(source, /采用 AI 推荐/);
  assert.match(source, /workflowCandidateScreening\(pendingDecisionStep\)/);
  assert.match(source, /workflowEvidenceScreening\(pendingDecisionStep\)/);
  assert.match(source, /以有限证据继续/);
  assert.match(source, /acceptLimitedEvidence: acceptLimited/);
});

test("completed research tasks distinguish frozen results from registered artifacts", async () => {
  const app = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(app, /artifactCount:\s*number/);
  assert.match(app, /registeredDeliverables\?: string\[\]/);
  assert.match(app, /workflowDeliverableRegistered\(runDetail, "report_draft"\)/);
  assert.match(app, /workflowDeliverableRegistered\(runDetail, "research_design"\)/);
  assert.match(app, /结果已保存，本次流程未生成文件型科研产物/);
  assert.match(app, /aria-label="打开科研产物"/);
});

test("completed research designs expose the real deliverable and trusted artifact registration", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /研究设计已完成并通过独立审查/);
  assert.match(source, /不是数据采集、模型训练或统计分析后的实证结论/);
  assert.match(source, /"WorkflowFacade", "SaveDeliverable"/);
  assert.match(source, /outputName: "research_design"/);
  assert.match(source, /研究设计已完成/);
  assert.match(source, /登记科研产物/);
});

test("completed empirical routes show the reviewed Markdown report before a design-only deliverable", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /function workflowResearchReport\(detail: WorkflowRunDetail\)/);
  assert.match(source, /const report = workflowResearchReport\(runDetail\), design = workflowResearchDesign\(runDetail\)/);
  const renderer = source.slice(source.indexOf("  renderTimelineRef.current ="), source.indexOf("  const timelineTaskId ="));
  const report = renderer.indexOf("if (report) return"), design = renderer.indexOf("if (design) return");
  assert.ok(report >= 0 && design > report);
  assert.match(renderer, /if \(report\) return [^\n]*<WorkflowResearchReportCard/);
  assert.match(renderer, /if \(design\) return [^\n]*<WorkflowResearchDesignCard/);
  assert.match(source, /function WorkflowResearchReportCard/);
  assert.match(source, /<ResearchReportLayers report=\{report\} renderMarkdown=\{text => <CitedMarkdown text=\{text\} citations=\{citations\}/);
  assert.match(source, /outputName === "report_draft"/);
  assert.match(source, /outputName \}/);
  assert.match(source, /runDetail\.deliveryAssessment\?\.label/);
  assert.doesNotMatch(source, /实证研究报告已完成/);
  assert.match(source, /登记本轮报告/);
});

test("research evidence keeps the reproduction snapshot collapsed and registration feedback prominent", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(source, /className="workflow-environment-manifest">\s*<details>/);
  assert.doesNotMatch(source, /className="workflow-environment-manifest">\s*<details open/);
  assert.match(source, /已登记 · 查看科研产物/);
  assert.match(source, /已登记 · 查看报告/);
  assert.match(styles, /\.research-evidence-panel > \.research-guide-feedback\s*\{[^}]*font-size:\s*13px/);
});

test("active research task keeps the route compact and scoped to the current task", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const styles = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.doesNotMatch(source, /className="research-run-switcher"/);
  assert.doesNotMatch(source, /<small>\{workflowStepLabels\[step\.status\]\}/);
  const routeSource = await readFile(new URL('./ResearchRoute.tsx', import.meta.url), 'utf8');
  assert.match(routeSource, /s\.attempt > 1 && <em title=\{`已执行 \$\{s\.attempt\} 次`\}>\{s\.attempt\}<\/em>/);
  assert.match(styles, /grid-template-rows: auto auto auto minmax\(150px,1fr\) auto/);
  assert.match(styles, /research-route-steps article \{[^}]*min-height: 41px/);
  assert.doesNotMatch(styles, /\.research-run-switcher/);
});

test("failed independent review enters a visible revision loop instead of retrying the gate", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(source, /function workflowReviewIssues\(detail: WorkflowRunDetail, gateStep: WorkflowStep\)/);
  assert.match(source, /builtin\.research\.workflow\.review\.gate/);
  assert.match(source, /报告需要修改/);
  assert.match(source, /<ResearchReviewFeedback/);
  assert.match(source, /<ResearchRevisionControls/);
  const controls = await readFile(new URL("./ResearchRevisionControls.tsx", import.meta.url), "utf8");
  assert.match(controls, /原始资料和旧版本保留/);
  assert.match(controls, /重新审查通过后才会交付/);
  assert.doesNotMatch(source, /reviewGateFailure && <div className="research-review-recovery"/);
  assert.match(source, /已进入修订回路/);
  assert.match(css, /\.research-review-issues\s*\{/);
  assert.match(css, /\.research-review-recovery\s*\{/);
});

test("research plan home centers the question entry with optional materials and history", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(source, /className="research-start-welcome"[\s\S]*className="research-start-materials"[\s\S]*<details className="research-start-examples">/);
  assert.match(source, /!detail \? <main className="workflow-plan-home research-start-home">/);
  assert.match(source, /className="research-start-center"[\s\S]*className="research-entry"[\s\S]*htmlFor="workspace-message-input"/);
  assert.doesNotMatch(source, /research-start-focus|在下方输入，开始对话/);
  assert.match(source, /aria-label="历史方案"[^\n]*setHistoryPlansOpen\(true\)/);
  assert.doesNotMatch(source, /workflow-template-gallery|workflow-plan-home-section|moveToPlanSection/);
  assert.doesNotMatch(source, /目标明确时，从经过验证的研究流程开始/);
  assert.match(css, /\.workflow-side\s*\{[^}]*overflow:\s*hidden/s);
  assert.match(css, /\.workflow-plan-home\s*\{[^}]*overflow-y:\s*auto/s);
  assert.match(css, /\.workflow-plan-home\s*\{[^}]*background:\s*#fff/s);
  assert.match(css, /\.mode-research \.workflow-task-home,\.mode-research \.workflow-plan-home\s*\{[^}]*background:\s*#fff/s);
  assert.match(css, /\.research-start-home\s*\{[^}]*flex-direction:\s*column/s);
  assert.match(css, /\.research-start-center\s*\{[^}]*width:\s*min\(820px,100%\)[^}]*align-self:\s*center[^}]*margin:\s*auto 0/s);
  assert.match(css, /\.research-plan-history-dialog\s*\{[^}]*max-height:\s*calc\(100vh - 100px\)/s);
});

test("frameless titlebar supports double-click maximize without handling control buttons", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /className="window-titlebar" onDoubleClick=/);
  assert.match(source, /closest\("\.window-controls"\)/);
  assert.match(source, /toggleMaximiseWindow\(\)/);
});

test("artifact and research materials preserve task scopes with focused detail dialogs", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(source, /function buildResourceScopes/);
  assert.match(source, /value\.scopeKind === "task" && value\.researchTaskId\?\.trim\(\)/);
  assert.match(source, /\.filter\(\(entry\) => entry\.count > 0\)/);
  assert.match(source, /function ResourceScopeNav/);
  assert.match(source, /function ResourceTreeFlow/);
  assert.match(source, /title: "输入 \/ 来源"/);
  assert.match(source, /title: "分析与过程"/);
  assert.match(source, /title: "研究成果"/);
  assert.match(source, /title: "正式导出"/);
  assert.match(source, /<ResearchMaterialsLibrary/);
  assert.doesNotMatch(source, /function KnowledgeLibrary\(/);
  assert.match(css, /\.resource-browser-layout\s*\{/);
  assert.match(css, /\.resource-tree-flow\.columns-4\s*\{[^}]*grid-template-columns:\s*repeat\(4,minmax\(0,1fr\)\)/s);
  assert.match(css, /\.resource-tree-flow\.columns-3\s*\{[^}]*grid-template-columns:\s*repeat\(3,minmax\(0,1fr\)\)/s);
  const materialsCSS = await readFile(new URL("./researchMaterialsLibrary.css", import.meta.url), "utf8");
  assert.match(materialsCSS, /\.research-materials-modal\s*\{/);
  assert.doesNotMatch(css, /\.knowledge-modal\s*\{/);
  assert.match(css, /\.resource-tree-flow\s*\{[^}]*height:\s*100%[^}]*overflow:\s*hidden/s);
  assert.match(css, /\.resource-tree-column\s*\{[^}]*height:\s*100%[^}]*overflow:\s*hidden/s);
  assert.match(source, /className="artifact-detail-dialog resource-detail-dialog"/);
  assert.match(css, /\.resource-detail-backdrop\s*\{/);
  assert.match(source, /全选当前归属/);
  assert.match(source, /批量移入回收站/);
  assert.match(source, /taskId\.trim\(\) \? "TrashTaskArtifact" : "TrashArtifact"/);
  assert.match(source, /const scopeWritable = !taskId\.trim\(\) \|\| selectedScopeKey === `task:\$\{taskId\.trim\(\)\}`/);
  assert.match(materialsCSS, /\.research-materials-list\s*\{/);
  assert.doesNotMatch(css, /\.(?:research-material-card|knowledge-technical-details)\s*\{/);
  assert.doesNotMatch(source, /String\(index \+ 1\)\.padStart/);
  assert.doesNotMatch(css, /\.resource-tree-column:not\(:last-child\)::(?:before|after)/);
});

test("new chat and research use high without inheriting an existing conversation override", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(source, /const defaultReasoningLevel: ReasoningLevel = "high"/);
  assert.match(source, /useState<ReasoningLevel>\(defaultReasoningLevel\)/);
  const change = source.split("async function changeReasoningLevel")[1].split("async function removeConversation")[0];
  assert.equal((change.match(/setWorkspaceReasoningLevel\(level\)/g) ?? []).length, 1);
  assert.match(change, /if \(!settingsConversation\) \{\s*setWorkspaceReasoningLevel\(level\)/);
});
