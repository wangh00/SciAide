import { CSSProperties, FormEvent, ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { eventsOn, minimiseWindow, quitApplication, toggleMaximiseWindow } from "./lib/wailsRuntime";

type Project = { id: string; name: string; description: string; workspacePath: string; workspaceKind: "managed" | "external" };
type PermissionMode = "plan" | "full_access";
type ReasoningLevel = "low" | "medium" | "high" | "xhigh" | "max";
type Conversation = { id: string; projectId: string; title: string; permissionMode: PermissionMode; reasoningLevel: ReasoningLevel };
type MessagePart = { type: string; text?: string };
type Message = { id: string; runId?: string; role: "user" | "assistant" | "system" | "tool"; status: string; parts: MessagePart[] };
type ProfileModel = { id: string; ownedBy?: string; enabled: boolean; isDefault: boolean; reasoningLevels: ReasoningLevel[]; reasoningCapabilitySource?: string };
type Profile = { id: string; name: string; baseUrl: string; modelId: string; models: ProfileModel[]; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; customHeaders: Record<string, string>; enabled: boolean; isDefault: boolean };
type AvailableModel = { id: string; ownedBy?: string };
type Run = { id: string; conversationId: string; status: string; errorMessage?: string; inputTokens: number; freshInputTokens: number; outputTokens: number; cachedInputTokens: number; cacheWriteTokens: number; cacheReportedTurns: number; cacheHitTurns: number; permissionMode: PermissionMode; requestedReasoningLevel: ReasoningLevel; resolvedReasoningLevel?: ReasoningLevel; contextWindowTokens: number; contextCompacted: boolean };
type UsageQuery = { startDate?: string; endDate?: string; modelProfileId?: string; modelId?: string };
type UsageSummary = { runCount: number; modelTurns: number; freshInputTokens: number; outputTokens: number; cacheReadTokens: number; cacheCreationTokens: number; realTotalTokens: number; cacheReportedTurns: number; cacheHitTurns: number; cacheHitRate: number; cacheDataAvailable: boolean };
type DailyUsage = UsageSummary & { date: string };
type ModelUsage = UsageSummary & { modelProfileId: string; profileName: string; modelId: string };
type UsageDashboardData = { query: UsageQuery; summary: UsageSummary; daily: DailyUsage[]; models: ModelUsage[] };
type PermissionRequirement = { kind: string; resource: string };
type ToolResult = { status: string; text: string; truncated: boolean; meta: { durationMillis?: number; originalBytes?: number } };
type ToolCall = { id: string; runId: string; toolName: string; toolVersion: string; arguments: unknown; status: string; risk: string; permissions: PermissionRequirement[]; errorMessage?: string; result?: ToolResult; createdAt: string; startedAt?: string; completedAt?: string };
type Approval = { id: string; runId: string; toolCallId: string; toolName: string; toolVersion: string; permissionKind: string; resource: string; risk: string; status: string; reason: string };
type RunSnapshot = { run: Run; messages: Message[]; toolCalls: ToolCall[]; pendingApprovals: Approval[] };
type MCPTransport = "stdio" | "streamable_http";
type MCPServer = { id: string; name: string; namespace: string; transport: MCPTransport; command: string; args: string[]; workingDir: string; url: string; headers: Record<string,string>; env: Record<string,string>; secretConfigured: Record<string,boolean>; enabled: boolean; autoStart: boolean; trust: "untrusted" | "user_trusted"; timeoutSeconds: number; status: string; protocolVersion?: string; serverVersion?: string; toolCount: number; resourceCount: number; promptCount: number; lastError?: string };
type MCPImportResult = { imported: MCPServer[]; errors: { name: string; message: string }[] };
type MCPBatchItem = { serverId: string; name?: string; status: "succeeded" | "skipped" | "failed"; message?: string; server: MCPServer };
type MCPBatchResult = { succeeded: number; skipped: number; failed: number; items: MCPBatchItem[] };
type MCPCapabilities = { protocolVersion?: string; serverVersion?: string; tools: { originalName: string; qualifiedName: string; description: string; version: string }[]; resources: string[]; prompts: string[] };
type Envelope = { aggregateId: string; type: string; payload: Record<string, unknown> };
type CreateDialog = { kind: "project" | "conversation"; title: string; description: string; workspacePath: string } | null;

declare global {
  interface Window { go?: { wails?: Record<string, Record<string, (...args: unknown[]) => Promise<unknown>>> } }
}

function backend<T>(facade: string, method: string, ...args: unknown[]): Promise<T> {
  const fn = window.go?.wails?.[facade]?.[method];
  if (!fn) return Promise.reject(new Error("Wails 后端尚未连接，请通过桌面程序或 wails dev 运行。"));
  return fn(...args) as Promise<T>;
}

function errorText(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  return "操作失败，请稍后重试。";
}

const textOf = (message: Message) => message.parts.filter((part) => part.type === "text").map((part) => part.text ?? "").join("");
const first = <T,>(items: T[]) => items[0];
const modelKey = (profileId: string, modelId: string) => `${profileId}\t${modelId}`;
const splitModelKey = (value: string): [string, string] => { const index = value.indexOf("\t"); return index < 0 ? ["", ""] : [value.slice(0, index), value.slice(index + 1)]; };
const reasoningLevels: ReasoningLevel[] = ["low", "medium", "high", "xhigh", "max"];
const inferredReasoningLevels = (modelId: string): ReasoningLevel[] => {
  const id = modelId.trim().toLowerCase();
  if (id.startsWith("o1")) return ["medium", "high"];
  if (id.startsWith("o3") || id.startsWith("o4") || id.includes("gpt-5")) return ["low", "medium", "high"];
  return [];
};
const messageRoleRank = (role: Message["role"]) => role === "user" ? 0 : role === "assistant" ? 1 : 2;
const orderedMessages = (values: Message[]) => values.map((message, index) => ({ message, index })).sort((left, right) => {
  if (left.message.runId && left.message.runId === right.message.runId) {
    const rank = messageRoleRank(left.message.role) - messageRoleRank(right.message.role);
    if (rank !== 0) return rank;
  }
  return left.index - right.index;
}).map(({ message }) => message);

function Icon({ name, size = 18 }: { name: "spark" | "plus" | "chat" | "settings" | "shield" | "model" | "send" | "stop" | "search" | "refresh" | "folder" | "check" | "close" | "trash" | "tool" | "server" | "chart"; size?: number }) {
  const paths: Record<typeof name, ReactNode> = {
    spark: <><path d="m12 2 1.35 4.15L17.5 7.5l-4.15 1.35L12 13l-1.35-4.15L6.5 7.5l4.15-1.35L12 2Z"/><path d="m5 14 .8 2.2L8 17l-2.2.8L5 20l-.8-2.2L2 17l2.2-.8L5 14Z"/></>,
    plus: <><path d="M12 5v14M5 12h14"/></>, chat: <path d="M21 15a4 4 0 0 1-4 4H8l-5 3V7a4 4 0 0 1 4-4h10a4 4 0 0 1 4 4v8Z"/>,
    settings: <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.09A1.7 1.7 0 0 0 9 19.36a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.63 15 1.7 1.7 0 0 0 3.08 14H3v-4h.09A1.7 1.7 0 0 0 4.64 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 9 4.63h.01A1.7 1.7 0 0 0 10 3.08V3h4v.09A1.7 1.7 0 0 0 15 4.64a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.37 9v.01A1.7 1.7 0 0 0 20.92 10H21v4h-.09A1.7 1.7 0 0 0 19.4 15Z"/></>,
    shield: <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/>, model: <><rect x="3" y="3" width="18" height="18" rx="5"/><path d="M8 9h8M8 12h5M8 15h7"/></>,
    send: <><path d="m22 2-7 20-4-9-9-4 20-7Z"/><path d="M22 2 11 13"/></>, stop: <rect x="6" y="6" width="12" height="12" rx="2"/>, search: <><circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/></>,
    refresh: <><path d="M20 11a8 8 0 1 0-2.34 5.66"/><path d="M20 4v7h-7"/></>, folder: <path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6Z"/>,
    check: <path d="m5 12 4 4L19 6"/>, close: <><path d="m6 6 12 12M18 6 6 18"/></>, trash: <><path d="M3 6h18M8 6V4h8v2M19 6l-1 15H6L5 6M10 11v5M14 11v5"/></>, tool: <><path d="M14.7 6.3a4 4 0 0 0-5 5L3 18l3 3 6.7-6.7a4 4 0 0 0 5-5l-2.2 2.2-3-3 2.2-2.2Z"/></>, server: <><rect x="4" y="3" width="16" height="7" rx="2"/><rect x="4" y="14" width="16" height="7" rx="2"/><path d="M8 6.5h.01M8 17.5h.01M12 6.5h5M12 17.5h5"/></>,
    chart: <><path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/></>,
  };
  return <svg aria-hidden="true" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">{paths[name]}</svg>;
}

export default function App() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState("");
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [conversationId, setConversationId] = useState("");
  const [messages, setMessages] = useState<Message[]>([]);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [profileId, setProfileId] = useState("");
  const [modelId, setModelId] = useState("");
  const [activeRun, setActiveRun] = useState<Run | null>(null);
  const [toolCalls, setToolCalls] = useState<ToolCall[]>([]);
  const [pendingApprovals, setPendingApprovals] = useState<Approval[]>([]);
  const [resolvingApprovalId, setResolvingApprovalId] = useState("");
  const [input, setInput] = useState("");
  const [notice, setNotice] = useState("");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [mcpOpen, setMcpOpen] = useState(false);
  const [usageOpen, setUsageOpen] = useState(false);
  const [createDialog, setCreateDialog] = useState<CreateDialog>(null);
  const [busy, setBusy] = useState(false);
  const activeRunRef = useRef<Run | null>(null);
  const conversationIdRef = useRef("");
  const modelSelectionRef = useRef({ profileId: "", modelId: "" });
  const chatRef = useRef<HTMLElement | null>(null);
  activeRunRef.current = activeRun;
  conversationIdRef.current = conversationId;
  modelSelectionRef.current = { profileId, modelId };

  const loadProjects = useCallback(async () => {
    const values = await backend<Project[]>("ProjectFacade", "ListProjects");
    setProjects(values); setProjectId((current) => current || first(values)?.id || "");
  }, []);
  const loadProfiles = useCallback(async () => {
    const values = await backend<Profile[]>("ModelFacade", "ListModelProfiles");
    setProfiles(values);
    const enabled = values.filter((item) => item.enabled).flatMap((profile) => profile.models.filter((model) => model.enabled).map((model) => ({ profile, model })));
    const fallback = enabled.find(({ profile, model }) => profile.isDefault && model.isDefault) ?? first(enabled);
    const current = modelSelectionRef.current;
    const candidates = enabled.filter(({ profile }) => profile.id === current.profileId);
    const next = candidates.find(({ model }) => model.id === current.modelId) ?? candidates.find(({ model }) => model.isDefault) ?? fallback;
    setProfileId(next?.profile.id ?? ""); setModelId(next?.model.id ?? "");
  }, []);
  const loadConversations = useCallback(async (selectedProject: string) => {
    if (!selectedProject) { setConversations([]); setConversationId(""); return; }
    const values = await backend<Conversation[]>("ConversationFacade", "ListConversations", selectedProject);
    setConversations(values); setConversationId((current) => values.some((item) => item.id === current) ? current : first(values)?.id || "");
  }, []);
  const loadMessages = useCallback(async (selectedConversation: string) => {
    if (!selectedConversation) { setMessages([]); return; }
    setMessages(orderedMessages(await backend<Message[]>("ConversationFacade", "ListMessages", selectedConversation)));
  }, []);
  const applySnapshot = useCallback((snapshot: RunSnapshot) => {
    if (snapshot.run.conversationId !== conversationIdRef.current) return;
    setActiveRun(snapshot.run);
    setToolCalls(snapshot.toolCalls ?? []);
    setPendingApprovals(snapshot.pendingApprovals ?? []);
    setMessages((current) => orderedMessages(snapshot.messages.map((message) => {
      const live = current.find((item) => item.id === message.id);
      return live && textOf(live).length > textOf(message).length ? live : message;
    })));
    setBusy(!["completed", "failed", "cancelled", "interrupted"].includes(snapshot.run.status));
  }, []);

  useEffect(() => { Promise.all([loadProjects(), loadProfiles()]).catch((error: unknown) => setNotice(errorText(error))); }, [loadProfiles, loadProjects]);
  useEffect(() => { loadConversations(projectId).catch((error: unknown) => setNotice(errorText(error))); }, [loadConversations, projectId]);
  useEffect(() => {
    setActiveRun(null); setToolCalls([]); setPendingApprovals([]); setBusy(false);
    if (!conversationId) { setMessages([]); return; }
    Promise.all([
      loadMessages(conversationId),
      backend<RunSnapshot | null>("ChatFacade", "GetLatestRunSnapshot", conversationId).then((snapshot) => { if (snapshot) applySnapshot(snapshot); }),
    ]).catch((error: unknown) => setNotice(errorText(error)));
  }, [applySnapshot, conversationId, loadMessages]);
  useEffect(() => { chatRef.current?.scrollTo({ top: chatRef.current.scrollHeight, behavior: "smooth" }); }, [messages]);

  useEffect(() => eventsOn<Envelope>("sciaide:run-event", (event) => {
    const run = activeRunRef.current;
    if (!run || event.aggregateId !== run.id) return;
    if (event.type === "content.delta") {
      const messageId = String(event.payload.messageId ?? ""); const delta = String(event.payload.delta ?? "");
      setMessages((current) => current.map((message) => message.id === messageId ? { ...message, parts: [{ type: "text", text: textOf(message) + delta }] } : message));
    }
    if (event.type.startsWith("run.") && event.payload.run) setActiveRun(event.payload.run as Run);
    if (event.type.startsWith("tool.") || event.type.startsWith("approval.")) {
      backend<RunSnapshot>("ChatFacade", "GetRunSnapshot", run.id).then(applySnapshot).catch(() => undefined);
    }
    if (["run.completed", "run.failed", "run.cancelled"].includes(event.type)) { setBusy(false); loadMessages(conversationId).catch(() => undefined); }
  }), [conversationId, loadMessages]);

  useEffect(() => {
    if (!activeRun || ["completed", "failed", "cancelled", "interrupted"].includes(activeRun.status)) return;
    const timer = window.setInterval(() => backend<RunSnapshot>("ChatFacade", "GetRunSnapshot", activeRun.id).then(applySnapshot).catch(() => undefined), 700);
    return () => window.clearInterval(timer);
  }, [activeRun?.id, activeRun?.status, applySnapshot]);

  async function submitCreate(event: FormEvent) {
    event.preventDefault(); if (!createDialog?.title.trim()) return;
    try {
      if (createDialog.kind === "project") {
        const created = await backend<Project>("ProjectFacade", "CreateProject", { name: createDialog.title.trim(), description: createDialog.description.trim(), workspacePath: createDialog.workspacePath.trim() });
        await loadProjects(); setProjectId(created.id);
      } else {
        const created = await backend<Conversation>("ConversationFacade", "CreateConversation", { projectId, title: createDialog.title.trim() });
        await loadConversations(projectId); setConversationId(created.id);
      }
      setCreateDialog(null);
    } catch (error) { setNotice(errorText(error)); }
  }

  async function send(event: FormEvent) {
    event.preventDefault(); const text = input.trim();
    if (!text || !conversationId || !profileId || !modelId) return;
    const runToSteer = busy && activeRun?.conversationId === conversationId ? activeRun : null;
    setNotice(""); setInput(""); setBusy(true);
    try {
      const command = { conversationId, modelProfileId: profileId, modelId, reasoningLevel: selectedConversation?.reasoningLevel ?? "medium", text };
      const run = runToSteer ? await backend<Run>("ChatFacade", "SteerChat", runToSteer.id, command) : await backend<Run>("ChatFacade", "StartChat", command);
      setActiveRun(run); const persistedConversation = await backend<Conversation>("ConversationFacade", "GetConversation", conversationId); setConversations((current) => current.map((item) => item.id === persistedConversation.id ? persistedConversation : item)); await loadMessages(conversationId);
    } catch (error) { setInput((current) => current || text); setBusy(false); setNotice(errorText(error)); }
  }

  const selectedProject = projects.find((item) => item.id === projectId);
  const selectedConversation = conversations.find((item) => item.id === conversationId);
  const selectedProfile = profiles.find((item) => item.id === profileId);
  const selectedModel = selectedProfile?.models.find((item) => item.id === modelId);
  const selectableModels = useMemo(() => profiles.filter((profile) => profile.enabled).flatMap((profile) => profile.models.filter((model) => model.enabled).map((model) => ({ profile, model }))), [profiles]);
  const selectedModelKey = profileId && modelId ? modelKey(profileId, modelId) : "";
  const usage = useMemo(() => activeRun ? `${activeRun.inputTokens} 输入 · ${activeRun.outputTokens} 输出${activeRun.cacheReportedTurns > 0 ? ` · ${activeRun.cachedInputTokens} 缓存命中` : ""} tokens` : "", [activeRun]);

  return <div className="app-shell">
	<div className="window-titlebar"><div className="window-brand"><span><Icon name="spark" size={13}/></span><b>SciAide</b></div><div className="window-controls"><button type="button" aria-label="最小化窗口" title="最小化" onClick={minimiseWindow}>—</button><button type="button" aria-label="最大化或还原窗口" title="最大化/还原" onClick={toggleMaximiseWindow}>□</button><button type="button" className="window-close" aria-label="关闭窗口" title="关闭" onClick={quitApplication}>×</button></div></div>
    <aside className="sidebar">
      <div className="logo"><span><Icon name="spark" size={21}/></span><div><strong>SciAide</strong><small>Research Copilot</small></div></div>
      <button className="new-project" onClick={() => setCreateDialog({ kind: "project", title: "", description: "", workspacePath: "" })}><Icon name="plus"/> 新建科研项目</button>
      <div className="project-block"><label className="field-label" htmlFor="project">WORKSPACE</label><div className="project-actions"><div className="select-shell"><Icon name="folder" size={16}/><select id="project" value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="">选择项目</option>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></div>{selectedProject && <button className="icon-danger" title="从 SciAide 移除项目" onClick={() => void removeProject(selectedProject)}><Icon name="trash" size={15}/></button>}</div>{selectedProject && <small className="workspace-path" title={selectedProject.workspacePath}>{selectedProject.workspaceKind === "external" ? "外部目录" : "SciAide 托管"} · {selectedProject.workspacePath}</small>}</div>
      <div className="section-title"><span>研究会话</span><button aria-label="新建会话" onClick={() => setCreateDialog({ kind: "conversation", title: "", description: "", workspacePath: "" })} disabled={!projectId}><Icon name="plus" size={17}/></button></div>
      <nav className="conversation-list">{conversations.length ? conversations.map((conversation) => <div className={`conversation-row ${conversation.id === conversationId ? "active" : ""}`} key={conversation.id}><button onClick={() => setConversationId(conversation.id)}><Icon name="chat" size={16}/><span>{conversation.title}</span></button><button className="conversation-remove" title="移除会话" onClick={() => void removeConversation(conversation)}><Icon name="close" size={13}/></button></div>) : <p className="sidebar-empty">{projectId ? "还没有会话，点击右上角 ＋ 创建" : "选择项目后显示会话"}</p>}</nav>
      <div className="sidebar-footer"><button onClick={() => setUsageOpen(true)}><span className="nav-icon"><Icon name="chart" size={17}/></span><span><b>用量统计</b><small>全部模型 · 日期与缓存命中</small></span></button><button onClick={() => setMcpOpen(true)}><span className="nav-icon"><Icon name="server" size={17}/></span><span><b>MCP Servers</b><small>连接科研工具与数据服务</small></span></button><button onClick={() => setSettingsOpen(true)}><span className="nav-icon"><Icon name="settings" size={17}/></span><span><b>模型与 API</b><small>{profiles.length ? `${profiles.length} 个配置可用` : "配置你的第一个模型"}</small></span><span className={selectedProfile?.secretConfigured ? "status-dot ready" : "status-dot"}/></button><div className="local-note"><Icon name="shield" size={13}/> 密钥由系统凭据库保护</div></div>
    </aside>

    <main className="workspace">
      <header className="topbar"><div className="breadcrumbs"><span>{selectedProject?.name ?? "Workspace"}</span><i>/</i><strong>{selectedConversation?.title ?? "新研究"}</strong></div><div className="top-actions"><div className="permission-picker" title={busy ? "运行期间不能切换权限模式" : "当前 Workspace 内只读免确认；外部读取、写入和其他工具需确认"}><Icon name="shield" size={13}/><select aria-label="工具权限模式" value={selectedConversation?.permissionMode ?? "plan"} disabled={!selectedConversation || busy} onChange={(event) => void changePermissionMode(event.target.value as PermissionMode)}><option value="plan">Plan · 写入/工具确认</option><option value="full_access">Full Access</option></select></div><div className="model-picker"><span className={selectedProfile?.secretConfigured ? "status-dot ready" : "status-dot"}/><select aria-label="选择模型" value={selectedModelKey} onChange={(event) => { const [nextProfile, nextModel] = splitModelKey(event.target.value); setProfileId(nextProfile); setModelId(nextModel); }}><option value="">选择模型</option>{selectableModels.map(({ profile, model }) => <option key={modelKey(profile.id, model.id)} value={modelKey(profile.id, model.id)}>{profile.name} · {model.id}</option>)}</select></div><div className="reasoning-picker" title={selectedModel?.reasoningLevels?.length ? `模型支持：${selectedModel.reasoningLevels.join(" / ")}；不支持时自动降到最近档位` : "该模型未启用推理参数，请到模型配置中选择支持档位"}><Icon name="spark" size={13}/><select aria-label="思考强度" value={selectedConversation?.reasoningLevel ?? "medium"} disabled={!selectedConversation || busy} onChange={(event) => void changeReasoningLevel(event.target.value as ReasoningLevel)}>{reasoningLevels.map((level) => <option value={level} key={level}>{level}</option>)}</select>{selectedModel && !selectedModel.reasoningLevels?.length && <i>off</i>}</div></div></header>
      <section className="chat" aria-live="polite" ref={chatRef}>
        {messages.length === 0 ? <EmptyState hasProject={Boolean(projectId)} hasConversation={Boolean(conversationId)} hasProfile={Boolean(profileId && modelId)} openSettings={() => setSettingsOpen(true)} createConversation={() => setCreateDialog({ kind: "conversation", title: "", description: "", workspacePath: "" })} setPrompt={setInput}/> : <div className="message-stack">{messages.map((message) => <article key={message.id} className={`message ${message.role}`}><div className="avatar">{message.role === "user" ? "你" : <Icon name="spark" size={17}/>}</div><div className="message-body"><div className="message-meta"><b>{message.role === "user" ? "你" : selectedProfile?.name ?? "SciAide"}</b>{message.status === "incomplete" && <span>生成已中断</span>}</div><div className="bubble">{textOf(message) || (message.status === "streaming" && activeRun?.status !== "waiting_approval" ? <span className="typing"><i/><i/><i/> 正在生成回答</span> : "")}</div>{message.role === "assistant" && message.runId === activeRun?.id && <RunActivity toolCalls={toolCalls} approvals={pendingApprovals} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>}</div></article>)}</div>}
      </section>
      <footer className="composer-wrap">{notice && <div className="notice"><Icon name="shield" size={15}/><span>{notice}</span><button onClick={() => setNotice("")}><Icon name="close" size={14}/></button></div>}{activeRun?.errorMessage && <div className="notice error run-error"><Icon name="shield" size={15}/><span>{activeRun.errorMessage}</span></div>}<form className="composer" onSubmit={(event) => void send(event)}><textarea value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} placeholder={!conversationId ? "请先创建或选择研究会话" : busy ? "输入新指令可中断当前生成并继续…" : "向 SciAide 描述你的研究问题…"} disabled={!conversationId || !profileId || !modelId}/><div className="composer-actions"><span>{busy ? "发送新消息将中断当前生成并立即继续" : usage || <><kbd>Enter</kbd> 发送 · <kbd>Shift Enter</kbd> 换行</>}</span><div className="composer-buttons">{busy && <button type="button" className="stop" onClick={() => activeRun && void backend<void>("ChatFacade", "CancelRun", activeRun.id).catch((error: unknown) => setNotice(errorText(error)))}><Icon name="stop" size={15}/> 停止</button>}<button className="send" aria-label={busy ? "中断并发送" : "发送"} disabled={!input.trim() || !conversationId || !profileId || !modelId}><Icon name="send" size={17}/></button></div></div></form><p className="composer-hint">AI 可能会出错，重要科研结论请核验原始来源。</p></footer>
    </main>
    {settingsOpen && <ModelSettings profiles={profiles} close={() => setSettingsOpen(false)} refresh={loadProfiles} select={setProfileId}/>}
    {mcpOpen && <MCPSettings close={() => setMcpOpen(false)}/>}
    {usageOpen && <UsageDashboard profiles={profiles} close={() => setUsageOpen(false)}/>}
    {createDialog && <CreateModal value={createDialog} setValue={setCreateDialog} close={() => setCreateDialog(null)} submit={submitCreate}/>}
  </div>;

  async function removeProject(value: Project) {
    const effect = value.workspaceKind === "managed" ? "托管目录会移至 ~/.sciaide/backups/trash，可手动恢复。" : "仅移除 SciAide 记录，外部目录及文件不会删除。";
    if (!window.confirm(`从 SciAide 移除“${value.name}”？\n\n${effect}\n项目下的会话和运行记录将删除。`)) return;
    try { await backend("ProjectFacade", "RemoveProject", value.id); setProjectId(""); setConversationId(""); setMessages([]); await loadProjects(); setNotice("项目已从 SciAide 移除。"); } catch (error) { setNotice(errorText(error)); }
  }

  async function resolveApproval(approval: Approval, allow: boolean) {
    if (resolvingApprovalId) return;
    setResolvingApprovalId(approval.id);
    try {
      await backend("PermissionFacade", "ResolveApproval", { approvalId: approval.id, allow, scope: "call" });
      applySnapshot(await backend<RunSnapshot>("ChatFacade", "GetRunSnapshot", approval.runId));
    } catch (error) { setNotice(errorText(error)); }
    finally { setResolvingApprovalId(""); }
  }

  async function changePermissionMode(mode: PermissionMode) {
    if (!selectedConversation || busy || selectedConversation.permissionMode === mode) return;
    try {
      const updated = await backend<Conversation>("ConversationFacade", "SetPermissionMode", selectedConversation.id, mode);
      setConversations((current) => current.map((item) => item.id === updated.id ? updated : item));
      setNotice(mode === "full_access" ? "已启用 Full Access：注册工具将自动执行。" : "已切换到 Plan：每次工具调用都需要确认。");
    } catch (error) { setNotice(errorText(error)); }
  }

  async function changeReasoningLevel(level: ReasoningLevel) {
    if (!selectedConversation || busy || selectedConversation.reasoningLevel === level) return;
    try {
      const updated = await backend<Conversation>("ConversationFacade", "SetReasoningLevel", selectedConversation.id, level);
      setConversations((current) => current.map((item) => item.id === updated.id ? updated : item));
      const resolved = selectedModel?.reasoningLevels?.length ? [...reasoningLevels].reverse().find((item) => reasoningLevels.indexOf(item) <= reasoningLevels.indexOf(level) && selectedModel.reasoningLevels.includes(item)) ?? selectedModel.reasoningLevels[0] : undefined;
      setNotice(!resolved ? "已保存思考偏好；当前模型未启用推理参数。" : resolved === level ? `思考强度已设为 ${level}。` : `已选择 ${level}；当前模型实际使用 ${resolved}。`);
    } catch (error) { setNotice(errorText(error)); }
  }

  async function removeConversation(value: Conversation) {
    if (!window.confirm(`移除研究会话“${value.title}”？\n\n该会话的消息和运行记录将删除，Workspace 文件不受影响。`)) return;
    try { await backend("ConversationFacade", "RemoveConversation", value.id); if (conversationId === value.id) { setConversationId(""); setMessages([]); } await loadConversations(projectId); setNotice("研究会话已移除。"); } catch (error) { setNotice(errorText(error)); }
  }
}

const toolStatusText: Record<string, string> = {
  pending: "已请求", awaiting_approval: "等待确认", running: "执行中", completed: "已完成",
  failed: "失败", denied: "已拒绝", cancelled: "已取消", interrupted: "已中断",
};

function RunActivity({ toolCalls, approvals, resolvingApprovalId, resolveApproval }: { toolCalls: ToolCall[]; approvals: Approval[]; resolvingApprovalId: string; resolveApproval: (approval: Approval, allow: boolean) => Promise<void> }) {
  if (!toolCalls.length && !approvals.length) return null;
  return <section className="run-activity" aria-label="工具调用时间线">
    {toolCalls.map((call) => {
      const approval = approvals.find((item) => item.toolCallId === call.id);
      const argumentText = JSON.stringify(call.arguments ?? {}, null, 2);
      return <article className={`tool-card ${call.status}`} key={call.id}>
        <header><span className="tool-icon"><Icon name="tool" size={15}/></span><div><b>{call.toolName}</b><small>v{call.toolVersion} · {toolStatusText[call.status] ?? call.status}</small></div><span className={`risk ${call.risk}`}>{call.risk}</span></header>
        <details><summary>查看参数与资源</summary><pre>{argumentText}</pre>{call.permissions.length > 0 && <div className="permission-list">{call.permissions.map((permission) => <span key={`${permission.kind}:${permission.resource}`}><b>{permission.kind}</b>{permission.resource || "全部资源"}</span>)}</div>}</details>
        {approval && <div className="approval-panel"><div><b>需要你的确认</b><p>Plan 模式下，本次工具调用只有在接受后才会执行。风险标签仅供参考，决定权完全属于你。</p></div><div className="approval-actions"><button disabled={Boolean(resolvingApprovalId)} onClick={() => void resolveApproval(approval, false)}>拒绝</button><button className="accept" disabled={Boolean(resolvingApprovalId)} onClick={() => void resolveApproval(approval, true)}>{resolvingApprovalId === approval.id ? "处理中…" : "Accept"}</button></div></div>}
        {call.result && <div className={`tool-result ${call.result.status}`}><span>{call.result.text || toolStatusText[call.status] || call.status}</span>{call.result.truncated && <small>结果已截断</small>}{call.result.meta?.durationMillis !== undefined && <small>{call.result.meta.durationMillis} ms</small>}</div>}
        {!call.result && call.errorMessage && <div className="tool-result error">{call.errorMessage}</div>}
      </article>;
    })}
  </section>;
}

function EmptyState({ hasProject, hasConversation, hasProfile, openSettings, createConversation, setPrompt }: { hasProject: boolean; hasConversation: boolean; hasProfile: boolean; openSettings: () => void; createConversation: () => void; setPrompt: (value: string) => void }) {
  const ready = hasProject && hasConversation && hasProfile;
  const action = !hasProfile ? openSettings : !hasConversation && hasProject ? createConversation : undefined;
  const prompts: Array<[string, string]> = [["研究假设", "帮我把当前研究问题拆成可检验的假设"], ["文献思路", "为这个研究主题梳理关键词和检索策略"], ["实验设计", "设计一套包含对照组的实验方案"]];
  return <div className="empty"><div className="ambient a"/><div className="ambient b"/><div className="ai-mark"><span/><Icon name="spark" size={32}/></div><div className="phase-pill"><i/> SCIENTIFIC AI WORKSPACE</div><h1>{ready ? "今天想探索什么？" : "构建你的科研工作空间"}</h1><p>{!hasProject ? "从左侧新建科研项目，SciAide 会把会话和运行记录组织在项目中。" : !hasConversation ? "创建一个研究会话，让问题、回答与后续产物保持连续。" : !hasProfile ? "连接你的模型 API。密钥只保存在系统凭据库中，不进入数据库。" : "从选题、文献思路到实验设计，把复杂问题拆成清晰的下一步。"}</p>{action && <button className="empty-action" onClick={action}>{!hasProfile ? <Icon name="model"/> : <Icon name="plus"/>}{!hasProfile ? "配置模型" : "创建研究会话"}</button>}{ready && <div className="prompt-grid">{prompts.map(([title,prompt]) => <button key={title} onClick={() => setPrompt(prompt)}><span><Icon name="spark" size={15}/></span><div><b>{title}</b><small>{prompt}</small></div><i>↗</i></button>)}</div>}</div>;
}

function CreateModal({ value, setValue, close, submit }: { value: Exclude<CreateDialog, null>; setValue: (value: CreateDialog) => void; close: () => void; submit: (event: FormEvent) => void }) {
  const project = value.kind === "project";
  async function chooseWorkspace() { try { const path = await backend<string>("ProjectFacade", "ChooseWorkspaceDirectory"); if (path) setValue({ ...value, workspacePath: path }); } catch { /* cancelled dialogs are harmless */ } }
  return <div className="modal-backdrop compact"><form className="create-modal" onSubmit={submit}><header><span className="dialog-icon"><Icon name={project ? "folder" : "chat"}/></span><div><h2>{project ? "新建科研项目" : "新建研究会话"}</h2><p>{project ? "集中管理一个研究方向下的会话与产物" : "围绕一个明确问题开始连续探索"}</p></div><button type="button" className="close" onClick={close}><Icon name="close"/></button></header><label>{project ? "项目名称" : "会话标题"}<input autoFocus value={value.title} onChange={(event) => setValue({ ...value, title: event.target.value })} placeholder={project ? "例如：单细胞转录组研究" : "例如：梳理实验假设"} maxLength={120} required/></label>{project && <><label>简要说明 <span>可选</span><textarea value={value.description} onChange={(event) => setValue({ ...value, description: event.target.value })} placeholder="记录研究目标或背景…" maxLength={500}/></label><label>Workspace 目录 <span>留空则保存到 ~/.sciaide/data/workspaces</span><div className="path-picker"><input value={value.workspacePath} onChange={(event) => setValue({ ...value, workspacePath: event.target.value })} placeholder="使用 SciAide 默认托管目录"/><button type="button" onClick={() => void chooseWorkspace()}><Icon name="folder" size={15}/> 选择文件夹</button></div></label></>}<footer><button type="button" onClick={close}>取消</button><button className="primary">创建</button></footer></form></div>;
}

function MCPSettings({ close }: { close: () => void }) {
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [id, setId] = useState("");
  const [toast, setToast] = useState<{ id: number; text: string; detail: string } | null>(null);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(() => new Set());
  const [batchResult, setBatchResult] = useState<MCPBatchResult | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [importJSON, setImportJSON] = useState("");
  const [importResult, setImportResult] = useState<MCPImportResult | null>(null);
  const current = servers.find((server) => server.id === id);
  const [name, setName] = useState("");
  const [namespace, setNamespace] = useState("");
  const [transport, setTransport] = useState<MCPTransport>("stdio");
  const [command, setCommand] = useState("");
  const [args, setArgs] = useState("");
  const [workingDir, setWorkingDir] = useState("");
  const [url, setUrl] = useState("");
  const [env, setEnv] = useState("");
  const [headers, setHeaders] = useState("");
  const [secretValues, setSecretValues] = useState("");
  const [clearSecrets, setClearSecrets] = useState<string[]>([]);
  const [trusted, setTrusted] = useState(false);
  const [enabled, setEnabled] = useState(true);
  const [feedback, setFeedback] = useState("");
  const [busy, setBusy] = useState(false);
  const [capabilities, setCapabilities] = useState<MCPCapabilities | null>(null);

  const refresh = useCallback(async () => {
    const values = await backend<MCPServer[]>("MCPFacade", "ListMCPServers");
    setServers(values);
    setSelectedIds((selected) => new Set([...selected].filter((serverId) => values.some((server) => server.id === serverId))));
  }, []);

  useEffect(() => {
    refresh().catch((error: unknown) => setFeedback(errorText(error)));
  }, [refresh]);

  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(null), 2600);
    return () => window.clearTimeout(timer);
  }, [toast]);

  useEffect(() => {
    setName(current?.name ?? "");
    setNamespace(current?.namespace ?? "");
    setTransport(current?.transport ?? "stdio");
    setCommand(current?.command ?? "");
    setArgs(current?.args?.join("\n") ?? "");
    setWorkingDir(current?.workingDir ?? "");
    setUrl(current?.url ?? "");
    setEnv(current && Object.keys(current.env).length ? JSON.stringify(current.env, null, 2) : "");
    setHeaders(current && Object.keys(current.headers).length ? JSON.stringify(current.headers, null, 2) : "");
    setSecretValues("");
    setClearSecrets([]);
    setTrusted(current?.trust === "user_trusted");
    setEnabled(current?.enabled ?? true);
    setFeedback("");
    setCapabilities(null);
    if (current?.status === "ready") {
      backend<MCPCapabilities>("MCPFacade", "GetMCPCapabilities", current.id)
        .then(setCapabilities)
        .catch(() => undefined);
    }
  }, [current]);

  function object(value: string, field: string) {
    const parsed = value.trim() ? JSON.parse(value) as unknown : {};
    if (!parsed || Array.isArray(parsed) || typeof parsed !== "object" || Object.values(parsed).some((item) => typeof item !== "string")) {
      throw new Error(`${field} 必须是字符串键值对 JSON 对象。`);
    }
    return parsed as Record<string, string>;
  }

  const configuredSecrets = Object.keys(current?.secretConfigured ?? {});

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setFeedback("");
    try {
      const saved = await backend<MCPServer>("MCPFacade", "SaveMCPServer", {
        id,
        name,
        namespace,
        transport,
        command: transport === "stdio" ? command : "",
        args: transport === "stdio" ? args.split(/\r?\n/).filter(Boolean) : [],
        workingDir: transport === "stdio" ? workingDir : "",
        url: transport === "streamable_http" ? url : "",
        headers: transport === "streamable_http" ? object(headers, "Headers") : {},
        env: transport === "stdio" ? object(env, "环境变量") : {},
        secretValues: transport === "stdio" ? object(secretValues, "SecretEnv") : {},
        clearSecrets: transport === "stdio" ? clearSecrets : [],
        enabled,
        autoStart: false,
        trust: trusted ? "user_trusted" : "untrusted",
        timeoutSeconds: current?.timeoutSeconds ?? 30,
      });
      await refresh();
      setId(saved.id);
      setSecretValues("");
      setClearSecrets([]);
      setToast({ id: Date.now(), text: "MCP 配置已保存", detail: "配置已安全写入，连接状态不会被自动改变。" });
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  async function importServers(event: FormEvent) {
    event.preventDefault();
    if (!importJSON.trim()) return;
    setBusy(true);
    setImportResult(null);
    setFeedback("");
    try {
      const result = await backend<MCPImportResult>("MCPFacade", "ImportMCPServers", { json: importJSON });
      setImportJSON("");
      setImportResult(result);
      await refresh();
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  async function connect() {
    if (!id) return;
    setBusy(true);
    setFeedback("正在初始化并发现 MCP 能力…");
    try {
      await backend<MCPServer>("MCPFacade", "ConnectMCPServer", id);
      await refresh();
      setCapabilities(await backend<MCPCapabilities>("MCPFacade", "GetMCPCapabilities", id));
      setFeedback("MCP Server 已连接，发现的工具已进入统一审批管道。");
    } catch (error) {
      setFeedback(errorText(error));
      await refresh();
    } finally {
      setBusy(false);
    }
  }

  async function disconnect() {
    if (!id) return;
    setBusy(true);
    try {
      await backend("MCPFacade", "DisconnectMCPServer", id);
      await refresh();
      setCapabilities(null);
      setFeedback("MCP Server 已断开，相关工具已从模型可用列表移除。");
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!id || !window.confirm("移除该 MCP Server 配置？系统凭据库中的关联 Secret 也会删除。")) return;
    setBusy(true);
    try {
      await backend("MCPFacade", "RemoveMCPServer", id);
      setSelectedIds((selected) => { const next = new Set(selected); next.delete(id); return next; });
      setId("");
      await refresh();
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  const activeStatuses = new Set(["ready", "starting", "initializing", "degraded", "stopping"]);
  const active = current ? activeStatuses.has(current.status) : false;
  const connectable = servers.filter((server) => server.enabled && server.trust === "user_trusted" && !activeStatuses.has(server.status));
  const selected = servers.filter((server) => selectedIds.has(server.id));
  const selectedConnectable = selected.filter((server) => server.enabled && server.trust === "user_trusted" && !activeStatuses.has(server.status));
  const selectedActive = selected.filter((server) => activeStatuses.has(server.status));
  const allConnectableSelected = connectable.length > 0 && connectable.every((server) => selectedIds.has(server.id));

  function toggleSelected(serverId: string) {
    setSelectedIds((values) => {
      const next = new Set(values);
      if (next.has(serverId)) next.delete(serverId); else next.add(serverId);
      return next;
    });
  }

  function toggleConnectable() {
    setSelectedIds((values) => {
      const next = new Set(values);
      if (allConnectableSelected) connectable.forEach((server) => next.delete(server.id));
      else connectable.forEach((server) => next.add(server.id));
      return next;
    });
  }

  async function batch(method: "ConnectMCPServers" | "DisconnectMCPServers", serverIds: string[]) {
    if (!serverIds.length) return;
    setBusy(true);
    setBatchResult(null);
    setFeedback(method === "ConnectMCPServers" ? `正在连接 ${serverIds.length} 个 MCP Server…` : `正在断开 ${serverIds.length} 个 MCP Server…`);
    try {
      const result = await backend<MCPBatchResult>("MCPFacade", method, { serverIds });
      setBatchResult(result);
      await refresh();
      if (id && method === "DisconnectMCPServers" && serverIds.includes(id)) setCapabilities(null);
      const action = method === "ConnectMCPServers" ? "连接" : "断开";
      setToast({ id: Date.now(), text: `批量${action}完成`, detail: `成功 ${result.succeeded} · 跳过 ${result.skipped} · 失败 ${result.failed}` });
      setFeedback(result.failed ? `批量${action}有 ${result.failed} 项失败，请查看左侧结果。` : `批量${action}已完成。`);
    } catch (error) {
      setFeedback(errorText(error));
      await refresh();
    } finally {
      setBusy(false);
    }
  }

  return <div className="modal-backdrop">
    <section className="model-modal mcp-modal" role="dialog" aria-modal="true">
      {toast && <div className="mcp-toast" role="status"><span><Icon name="check" size={15}/></span><div><b>{toast.text}</b><small>{toast.detail}</small></div></div>}
      <header>
        <div><span className="dialog-icon gradient"><Icon name="server"/></span><div><p>MODEL CONTEXT PROTOCOL</p><h2>MCP Servers</h2></div></div>
        <button className="close" onClick={close} aria-label="关闭"><Icon name="close"/></button>
      </header>
      <div className="settings-grid">
        <aside>
          <button className={`add-profile ${!importOpen && !id ? "selected" : ""}`} onClick={() => { setImportOpen(false); setId(""); }}><Icon name="plus"/> 添加 MCP Server</button>
          <button className={`add-profile import-profile ${importOpen ? "selected" : ""}`} onClick={() => { setImportOpen(true); setFeedback(""); setImportResult(null); }}><Icon name="tool"/> 从 JSON 导入</button>
          {servers.length > 0 && <div className="mcp-batch-panel">
            <div><button type="button" className="mcp-select-all" onClick={toggleConnectable} disabled={busy || !connectable.length}><span className={`mcp-check ${allConnectableSelected ? "checked" : ""}`}>{allConnectableSelected && <Icon name="check" size={11}/>}</span>{allConnectableSelected ? "取消全选" : "全选可连接"}</button><small>已选 {selected.length}</small></div>
            <button type="button" className="mcp-connect-all" onClick={() => void batch("ConnectMCPServers", connectable.map((server) => server.id))} disabled={busy || !connectable.length}><Icon name="server" size={14}/> 一键连接全部 <span>{connectable.length}</span></button>
            <div className="mcp-selected-actions"><button type="button" onClick={() => void batch("ConnectMCPServers", selectedConnectable.map((server) => server.id))} disabled={busy || !selectedConnectable.length}>连接所选</button><button type="button" onClick={() => void batch("DisconnectMCPServers", selectedActive.map((server) => server.id))} disabled={busy || !selectedActive.length}>断开所选</button></div>
          </div>}
          {batchResult && <div className="mcp-batch-result"><b>最近批量操作</b><span>成功 {batchResult.succeeded} · 跳过 {batchResult.skipped} · 失败 {batchResult.failed}</span>{batchResult.items.filter((item) => item.status !== "succeeded").map((item) => <p className={item.status} key={`${item.serverId}-${item.status}`}><strong>{item.name || item.serverId || "未知 Server"}</strong><small>{item.message || item.status}</small></p>)}</div>}
          <div className="profile-caption">已配置</div>
          {servers.map((server) => <div className={`mcp-profile-row ${selectedIds.has(server.id) ? "checked" : ""}`} key={server.id}><button type="button" className="mcp-row-check" aria-label={`选择 ${server.name}`} aria-pressed={selectedIds.has(server.id)} onClick={() => toggleSelected(server.id)} disabled={busy}><span className={`mcp-check ${selectedIds.has(server.id) ? "checked" : ""}`}>{selectedIds.has(server.id) && <Icon name="check" size={11}/>}</span></button><button className={`profile-item ${!importOpen && server.id === id ? "selected" : ""}`} onClick={() => { setImportOpen(false); setId(server.id); }} disabled={busy}>
            <span className="provider-logo"><Icon name="server" size={15}/></span>
            <span><b>{server.name}</b><small>{server.transport} · {server.toolCount} tools</small></span>
            <i className={`status-dot ${server.status === "ready" ? "ready" : server.status === "failed" ? "failed" : ""}`}/>
          </button></div>)}
        </aside>
        <form onSubmit={(event) => importOpen ? void importServers(event) : void save(event)}>
          {importOpen ? <>
          <section className="form-section mcp-import-section">
            <div className="form-heading"><span>JSON</span><div><h3>导入 MCP 配置</h3><p>兼容 Claude Desktop、Cursor、Codex 等常见的 mcpServers 结构</p></div></div>
            <div className="mcp-import-note"><Icon name="shield" size={17}/><div><b>导入不等于执行</b><span>配置会保存为“不受信任”且不会自动连接。请检查命令后，再手动确认信任并连接。</span></div></div>
            <label>mcpServers JSON<textarea className="mcp-import-editor" autoFocus spellCheck={false} value={importJSON} onChange={(event) => setImportJSON(event.target.value)} placeholder={'{\n  "mcpServers": {\n    "chrome-devtools": {\n      "command": "npx",\n      "args": ["-y", "chrome-devtools-mcp@latest"]\n    }\n  }\n}'} required disabled={busy}/><small>支持一次导入多个 Server；args 保持数组并直接传给进程，不经过 Shell 拼接。</small></label>
            <div className="mcp-import-security"><b>敏感信息如何保存？</b><p><code>env</code> 中名称包含 TOKEN、SECRET、PASSWORD、API_KEY、AUTH、CREDENTIAL 或 COOKIE 的值，会自动写入 Windows Credential Manager，不进入 SQLite。</p></div>
          </section>
          {importResult && <section className="mcp-import-result">
            {importResult.imported.length > 0 && <div className="imported"><b><Icon name="check" size={15}/> 已导入 {importResult.imported.length} 个 Server</b>{importResult.imported.map((server) => <button type="button" key={server.id} onClick={() => { setImportOpen(false); setId(server.id); }}><span><strong>{server.name}</strong><small>{server.transport} · {server.namespace}</small></span><i>检查配置 →</i></button>)}</div>}
            {importResult.errors.length > 0 && <div className="import-errors"><b>有 {importResult.errors.length} 项未导入</b>{importResult.errors.map((error, index) => <p key={`${error.name}-${index}`}><strong>{error.name || "未命名 Server"}</strong><span>{error.message}</span></p>)}</div>}
          </section>}
          {feedback && <div className="feedback error">{feedback}</div>}
          <footer className="modal-actions mcp-import-actions"><span/><span/><button type="button" onClick={() => { setImportJSON(""); setImportResult(null); }} disabled={busy || (!importJSON && !importResult)}>清空</button><button className="primary" disabled={busy || !importJSON.trim()}>{busy ? "导入中…" : "解析并导入"}</button></footer>
          </> : <>
          <section className="form-section">
            <div className="form-heading"><span>01</span><div><h3>连接配置</h3><p>stdio 直接启动程序；HTTP 使用 MCP Streamable HTTP 协议</p></div></div>
            <div className="form-row two">
              <label>名称<input value={name} onChange={(event) => setName(event.target.value)} required maxLength={100}/></label>
              <label>稳定命名空间<input value={namespace} onChange={(event) => setNamespace(event.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ""))} placeholder="zotero" required maxLength={32} disabled={active}/></label>
            </div>
            <label>Transport<select value={transport} onChange={(event) => setTransport(event.target.value as MCPTransport)} disabled={active}><option value="stdio">Local · stdio</option><option value="streamable_http">Remote · Streamable HTTP</option></select></label>
            {transport === "stdio" ? <>
              <label>Command<input value={command} onChange={(event) => setCommand(event.target.value)} placeholder={'node 或 C:\\tools\\mcp-server.exe'} required disabled={active}/></label>
              <label>Args <small>每行一个参数，不经过 Shell 拼接</small><textarea value={args} onChange={(event) => setArgs(event.target.value)} placeholder={'server.js\n--stdio'} disabled={active}/></label>
              <label>Working Directory <small>可选，必须是绝对路径</small><input value={workingDir} onChange={(event) => setWorkingDir(event.target.value)} disabled={active}/></label>
              <label>非敏感环境变量（JSON）<textarea value={env} onChange={(event) => setEnv(event.target.value)} placeholder={'{"LANG":"zh_CN.UTF-8"}'} disabled={active}/></label>
              <label>敏感环境变量（仅设置/替换）<textarea value={secretValues} onChange={(event) => setSecretValues(event.target.value)} placeholder={'{"ZOTERO_API_KEY":"secret"}'} disabled={active}/><small>明文只提交到后端并写入 Windows Credential Manager，不进入 SQLite。</small></label>
              {configuredSecrets.length > 0 && <div className="secret-chips"><b>已保护 SecretEnv</b>{configuredSecrets.map((key) => <button type="button" className={clearSecrets.includes(key) ? "clearing" : ""} onClick={() => setClearSecrets((values) => values.includes(key) ? values.filter((item) => item !== key) : [...values, key])} disabled={active} key={key}><code>{key}</code>{clearSecrets.includes(key) ? "将清除" : "已配置"}</button>)}</div>}
            </> : <>
              <label>MCP Endpoint<input value={url} onChange={(event) => setUrl(event.target.value)} placeholder="https://example.org/mcp" required disabled={active}/></label>
              <label>非敏感 Headers（JSON）<textarea value={headers} onChange={(event) => setHeaders(event.target.value)} placeholder={'{"X-Tenant":"lab"}'} disabled={active}/><small>Authorization、Cookie、Token、Secret、API-Key 等敏感 Header 会被拒绝持久化。</small></label>
              {url.startsWith("http://") && <div className="local-http-warning"><Icon name="shield" size={15}/> 明文 HTTP 仅允许 localhost/回环地址，请勿承载敏感研究数据。</div>}
            </>}
          </section>
          <section className="form-section">
            <div className="form-heading"><span>02</span><div><h3>信任与能力</h3><p>服务器描述、ToolResult、Resource 和 Prompt 均视为不可信数据</p></div></div>
            <label className="trust-row"><input type="checkbox" checked={trusted} onChange={(event) => setTrusted(event.target.checked)} disabled={active}/><span>我确认信任此 Server 配置及其进程/远程端点</span></label>
            <label className="trust-row"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} disabled={active}/><span>启用此 Server</span></label>
            <div className={`mcp-lifecycle-note ${active ? "connected" : ""}`}><Icon name="server" size={16}/><div><b>{active ? "当前连接已启用" : "保存配置不会启动进程"}</b><span>{active ? "工具已注册给模型；断开或退出 SciAide 时会卸载工具并关闭 MCP。" : "点击“连接并启用”完成能力发现后，模型才能使用该 Server 的工具。"}</span></div></div>
            {current && <div className="mcp-status"><b className={current.status}>{current.status}</b><span>{current.toolCount} Tools · {current.resourceCount} Resources · {current.promptCount} Prompts</span>{(current.protocolVersion || current.serverVersion) && <code>MCP {current.protocolVersion || "?"} · Server {current.serverVersion || "?"}</code>}{current.lastError && <small>{current.lastError}</small>}</div>}
            {capabilities && <div className="mcp-capabilities"><b>已注册工具</b>{capabilities.tools.map((item) => <span key={item.qualifiedName}><code>{item.qualifiedName}</code><small>{item.originalName}</small></span>)}{!capabilities.tools.length && <p>该 Server 未提供工具。</p>}<p>Resources / Prompts 仅发现与展示，不会自动注入对话上下文。</p></div>}
          </section>
          {feedback && <div className="feedback info">{feedback}</div>}
          <footer className="modal-actions">
            {id && <button type="button" className="danger" onClick={() => void remove()} disabled={busy || active}>删除</button>}
            <span/>
            {id && active ? <button type="button" onClick={() => void disconnect()} disabled={busy}>断开</button> : id && <button type="button" onClick={() => void connect()} disabled={busy || !trusted || !enabled}>连接并启用</button>}
            <button className="primary" disabled={busy || active}>{busy ? "处理中…" : "保存配置"}</button>
          </footer>
          </>}
        </form>
      </div>
    </section>
  </div>;
}

const localDateValue = (value: Date) => {
  const year = value.getFullYear();
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
};
const usageNumber = (value: number) => value.toLocaleString();
const usageRate = (summary: UsageSummary) => summary.cacheDataAvailable ? `${(summary.cacheHitRate * 100).toFixed(1)}%` : "未报告";

function UsageDashboard({ profiles, close }: { profiles: Profile[]; close: () => void }) {
  const [range, setRange] = useState<"all" | "today" | "7d" | "14d" | "30d" | "custom">("all");
  const [customStart, setCustomStart] = useState("");
  const [customEnd, setCustomEnd] = useState("");
  const [profileFilter, setProfileFilter] = useState("");
  const [modelFilter, setModelFilter] = useState("");
  const [data, setData] = useState<UsageDashboardData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const query = useMemo<UsageQuery>(() => {
    const today = new Date();
    let startDate = ""; let endDate = "";
    if (range === "today") startDate = endDate = localDateValue(today);
    if (range === "7d" || range === "14d" || range === "30d") {
      const start = new Date(today.getFullYear(), today.getMonth(), today.getDate());
      start.setDate(start.getDate() - (range === "7d" ? 6 : range === "14d" ? 13 : 29));
      startDate = localDateValue(start); endDate = localDateValue(today);
    }
    if (range === "custom") { startDate = customStart; endDate = customEnd; }
    return { startDate, endDate, modelProfileId: profileFilter, modelId: modelFilter };
  }, [customEnd, customStart, modelFilter, profileFilter, range]);

  const load = useCallback(async () => {
    setLoading(true); setError("");
    try { setData(await backend<UsageDashboardData>("ChatFacade", "GetUsageDashboard", query)); }
    catch (reason) { setError(errorText(reason)); }
    finally { setLoading(false); }
  }, [customEnd, customStart, query, range]);
  useEffect(() => { void load(); }, [load]);

  const modelOptions = useMemo(() => Array.from(new Set(profiles.flatMap((profile) => profile.models.map((model) => model.id)))).sort(), [profiles]);
  const summary = data?.summary;
  const maxDaily = Math.max(1, ...(data?.daily.map((item) => item.realTotalTokens) ?? [1]));

  return <div className="modal-backdrop"><section className="usage-modal" role="dialog" aria-modal="true" aria-labelledby="usage-title">
    <header><div><span className="dialog-icon gradient"><Icon name="chart"/></span><div><p>CLIENT-WIDE ANALYTICS</p><h2 id="usage-title">用量与缓存统计</h2></div></div><button className="close" onClick={close}><Icon name="close"/></button></header>
    <div className="usage-content">
      <div className="usage-filterbar">
        <div className="usage-presets">{(["all", "today", "7d", "14d", "30d", "custom"] as const).map((item) => <button type="button" className={range === item ? "active" : ""} onClick={() => setRange(item)} key={item}>{item === "all" ? "全部" : item === "today" ? "今天" : item === "7d" ? "近 7 天" : item === "14d" ? "近 14 天" : item === "30d" ? "近 30 天" : "自定义"}</button>)}</div>
        <div className="usage-dimensions"><label>API 配置<select value={profileFilter} onChange={(event) => setProfileFilter(event.target.value)}><option value="">全部配置</option>{profiles.map((profile) => <option value={profile.id} key={profile.id}>{profile.name}</option>)}</select></label><label>模型<select value={modelFilter} onChange={(event) => setModelFilter(event.target.value)}><option value="">全部模型</option>{modelOptions.map((model) => <option value={model} key={model}>{model}</option>)}</select></label><button type="button" className="usage-refresh" onClick={() => void load()} title="刷新"><Icon name="refresh" size={15}/></button></div>
      </div>
      {range === "custom" && <div className="usage-custom-range"><label>开始日期<input type="date" value={customStart} onChange={(event) => setCustomStart(event.target.value)}/></label><span>至</span><label>结束日期<input type="date" value={customEnd} onChange={(event) => setCustomEnd(event.target.value)}/></label></div>}
      {error && <div className="feedback error"><span>{error}</span></div>}
      {loading && !data ? <div className="usage-page-loading">正在汇总全部模型用量…</div> : summary && <>
        <section className="usage-hero"><div className="usage-total"><span>真实总 Token</span><strong>{usageNumber(summary.realTotalTokens)}</strong><small>实际输入 + 输出 + 缓存读取 + 缓存创建</small></div><div className="hit-rate-ring" style={{ "--hit-rate": `${summary.cacheHitRate * 100}%` } as CSSProperties}><div><strong>{usageRate(summary)}</strong><span>缓存命中率</span></div></div><div className="usage-context"><span>{summary.runCount.toLocaleString()} 次运行</span><span>{summary.modelTurns.toLocaleString()} 个模型轮次</span><small>{summary.cacheDataAvailable ? `${summary.cacheReportedTurns} 个轮次返回了缓存明细` : "当前服务尚未返回缓存明细"}</small></div></section>
        <section className="usage-token-grid"><article><span>实际输入</span><b>{usageNumber(summary.freshInputTokens)}</b><small>排除缓存后的新输入</small></article><article><span>模型输出</span><b>{usageNumber(summary.outputTokens)}</b><small>模型生成 Token</small></article><article className="cache-read"><span>缓存读取</span><b>{usageNumber(summary.cacheReadTokens)}</b><small>本次命中的输入缓存</small></article><article className="cache-create"><span>缓存创建</span><b>{usageNumber(summary.cacheCreationTokens)}</b><small>写入供后续复用的缓存</small></article></section>
        <div className="usage-panels">
          <section className="usage-panel"><div className="usage-panel-title"><div><h3>日期趋势</h3><p>按系统本地日期汇总真实 Token</p></div></div>{data.daily.length ? <div className="usage-trend">{data.daily.map((item) => <div className="trend-row" key={item.date}><time>{item.date}</time><div className="trend-track"><i style={{ width: `${Math.max(2, item.realTotalTokens / maxDaily * 100)}%` }}/></div><b>{usageNumber(item.realTotalTokens)}</b><span>{usageRate(item)}</span></div>)}</div> : <div className="usage-empty">所选日期范围内还没有用量记录</div>}</section>
          <section className="usage-panel model-usage-panel"><div className="usage-panel-title"><div><h3>按模型统计</h3><p>百分比由各模型 Token 汇总后独立计算</p></div></div>{data.models.length ? <div className="usage-table"><div className="usage-table-head"><span>模型 / API</span><span>实际输入</span><span>缓存读取</span><span>输出</span><span>命中率</span></div>{data.models.map((item) => <div className="usage-table-row" key={`${item.modelProfileId}\t${item.modelId}`}><span><b>{item.modelId}</b><small>{item.profileName || "未知配置"}</small></span><span>{usageNumber(item.freshInputTokens)}</span><span>{usageNumber(item.cacheReadTokens)}</span><span>{usageNumber(item.outputTokens)}</span><span className={item.cacheDataAvailable ? "rate" : "muted"}>{usageRate(item)}</span></div>)}</div> : <div className="usage-empty">没有匹配的模型记录</div>}</section>
        </div>
        <p className="usage-method"><Icon name="shield" size={13}/> OpenAI-compatible 的 <code>prompt_tokens</code> 会先扣除缓存读取与创建，得到“实际输入”。命中率 = 缓存读取 ÷（实际输入 + 缓存创建 + 缓存读取）；未返回缓存字段的轮次不会被误算为未命中。</p>
      </>}
    </div>
  </section></div>;
}

function ModelSettings({ profiles, close, refresh, select }: { profiles: Profile[]; close: () => void; refresh: () => Promise<void>; select: (id: string) => void }) {
  const [id, setId] = useState(""); const current = profiles.find((item) => item.id === id);
  const [name, setName] = useState(""); const [baseUrl, setBaseUrl] = useState("https://api.openai.com/v1"); const [profileModels, setProfileModels] = useState<ProfileModel[]>([]); const [manualModelId, setManualModelId] = useState(""); const [apiKey, setApiKey] = useState(""); const [headers, setHeaders] = useState("");
  const [models, setModels] = useState<AvailableModel[]>([]); const [modelSearch, setModelSearch] = useState(""); const [discovering, setDiscovering] = useState(false); const [feedback, setFeedback] = useState<{ kind: "ok" | "error" | "info"; text: string } | null>(null); const [saving, setSaving] = useState(false);
  useEffect(() => { setName(current?.name ?? ""); setBaseUrl(current?.baseUrl ?? "https://api.openai.com/v1"); setProfileModels(current?.models ?? []); setManualModelId(""); setApiKey(""); setHeaders(current && Object.keys(current.customHeaders).length ? JSON.stringify(current.customHeaders) : ""); setModels([]); setModelSearch(""); setFeedback(null); }, [current]);
  const filteredModels = useMemo(() => models.filter((item) => item.id.toLowerCase().includes(modelSearch.trim().toLowerCase())), [modelSearch, models]);
  function parsedHeaders() { return headers.trim() ? JSON.parse(headers) as Record<string, string> : {}; }
  function toggleModel(item: AvailableModel) { setProfileModels((currentModels) => { const exists = currentModels.some((model) => model.id === item.id); if (exists) { const remaining = currentModels.filter((model) => model.id !== item.id); return remaining.map((model, index) => ({ ...model, isDefault: index === 0 ? !remaining.some((candidate) => candidate.isDefault) : model.isDefault })); } return [...currentModels, { id: item.id, ownedBy: item.ownedBy, enabled: true, isDefault: currentModels.length === 0, reasoningLevels: inferredReasoningLevels(item.id), reasoningCapabilitySource: inferredReasoningLevels(item.id).length ? "inferred" : "unsupported" }]; }); }
  function addManualModel() { const value = manualModelId.trim(); if (!value || profileModels.some((model) => model.id === value)) return; const inferred = inferredReasoningLevels(value); setProfileModels((currentModels) => [...currentModels, { id: value, enabled: true, isDefault: currentModels.length === 0, reasoningLevels: inferred, reasoningCapabilitySource: inferred.length ? "inferred" : "unsupported" }]); setManualModelId(""); }
  function setDefaultModel(modelId: string) { setProfileModels((currentModels) => currentModels.map((model) => ({ ...model, enabled: true, isDefault: model.id === modelId }))); }
  function toggleReasoningLevel(modelId: string, level: ReasoningLevel) { setProfileModels((currentModels) => currentModels.map((model) => model.id !== modelId ? model : { ...model, reasoningLevels: model.reasoningLevels.includes(level) ? model.reasoningLevels.filter((item) => item !== level) : reasoningLevels.filter((item) => item === level || model.reasoningLevels.includes(item)), reasoningCapabilitySource: "manual" })); }
  async function discover() { setDiscovering(true); setFeedback({ kind: "info", text: "正在读取 /v1/models…" }); try { const values = await backend<AvailableModel[]>("ModelFacade", "DiscoverModels", { profileId: id, baseUrl, apiKey, customHeaders: parsedHeaders() }); setModels(values); setFeedback(values.length ? { kind: "ok", text: `已获取 ${values.length} 个模型，可勾选多个模型共用该 API Key。` } : { kind: "info", text: "服务返回了空列表，请手动添加 Model ID。" }); } catch (error) { setModels([]); setFeedback({ kind: "error", text: errorText(error) }); } finally { setDiscovering(false); } }
  async function save(event: FormEvent) { event.preventDefault(); const defaultModel = profileModels.find((model) => model.isDefault) ?? profileModels[0]; if (!defaultModel) { setFeedback({ kind: "error", text: "请至少选择或手动添加一个模型。" }); return; } setSaving(true); setFeedback(null); try { const saved = await backend<Profile>("ModelFacade", "SaveModelProfile", { id, name, baseUrl, modelId: defaultModel.id, models: profileModels, apiKey, timeoutSeconds: current?.timeoutSeconds ?? 60, customHeaders: parsedHeaders(), enabled: true, isDefault: profiles.length === 0 || current?.isDefault === true }); await refresh(); setId(saved.id); select(saved.id); setApiKey(""); setFeedback({ kind: "ok", text: `配置和 ${profileModels.length} 个模型已安全保存。` }); } catch (error) { setFeedback({ kind: "error", text: errorText(error) }); } finally { setSaving(false); } }
  async function test() { if (!id) return; setFeedback({ kind: "info", text: "正在验证连接…" }); try { await backend<void>("ModelFacade", "TestModelConnection", id); setFeedback({ kind: "ok", text: "连接成功，模型服务可访问。" }); } catch (error) { setFeedback({ kind: "error", text: errorText(error) }); } }
  async function remove() { if (!id || !window.confirm("删除该 API 配置及系统凭据？若聊天历史仍引用该配置，SciAide 会拒绝删除。")) return; try { await backend<void>("ModelFacade", "DeleteModelProfile", id); setId(""); await refresh(); setFeedback({ kind: "ok", text: "模型配置已删除。" }); } catch (error) { setFeedback({ kind: "error", text: errorText(error) }); } }
  return <div className="modal-backdrop"><section className="model-modal" role="dialog" aria-modal="true" aria-labelledby="model-title"><header><div><span className="dialog-icon gradient"><Icon name="model"/></span><div><p>MODEL GATEWAY</p><h2 id="model-title">模型与 API</h2></div></div><button className="close" onClick={close}><Icon name="close"/></button></header><div className="settings-grid"><aside><button className={`add-profile ${!id ? "selected" : ""}`} onClick={() => setId("")}><Icon name="plus"/> 添加 API 配置</button><div className="profile-caption">已保存</div>{profiles.map((profile) => <button className={`profile-item ${profile.id === id ? "selected" : ""}`} onClick={() => setId(profile.id)} key={profile.id}><span className="provider-logo">AI</span><span><b>{profile.name}</b><small>{profile.models.length} 个模型 · {profile.modelId}</small></span><i className={profile.secretConfigured ? "status-dot ready" : "status-dot"}/></button>)}</aside><form onSubmit={(event) => void save(event)}><section className="form-section"><div className="form-heading"><span>01</span><div><h3>服务连接</h3><p>一个 Base URL 与 API Key 可关联多个模型</p></div></div><div className="form-row two"><label>配置名称<input value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：实验室模型服务" required/></label><label>API Key <small>{current?.secretConfigured ? `已保存 ${current.secretMasked}` : "本地服务可留空"}</small><input type="password" autoComplete="new-password" value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder={current?.secretConfigured ? "留空保持现有密钥" : "sk-…"}/></label></div><label>Base URL<div className="endpoint-row"><input value={baseUrl} onChange={(event) => { setBaseUrl(event.target.value); setModels([]); }} placeholder="https://api.openai.com/v1" required/><button type="button" className="discover" onClick={() => void discover()} disabled={discovering || !baseUrl.trim()}><Icon name="refresh" size={15}/>{discovering ? "获取中" : "获取模型"}</button></div><small className="field-help">SciAide 将请求 <code>{baseUrl.replace(/\/$/, "") || "{Base URL}"}/models</code></small></label></section><section className="form-section"><div className="form-heading"><span>02</span><div><h3>可用模型</h3><p>勾选多个模型，并指定聊天默认模型</p></div></div>{models.length > 0 && <div className="model-browser"><div className="model-search"><Icon name="search" size={16}/><input value={modelSearch} onChange={(event) => setModelSearch(event.target.value)} placeholder={`搜索 ${models.length} 个模型`}/></div><div className="model-results">{filteredModels.slice(0, 80).map((item) => { const selected = profileModels.some((model) => model.id === item.id); return <button type="button" className={selected ? "selected" : ""} key={item.id} onClick={() => toggleModel(item)}><span className="checkbox">{selected && <Icon name="check" size={11}/>}</span><b>{item.id}</b><small>{item.ownedBy || "OpenAI-compatible"}</small>{selected && <span className="chosen">已选</span>}</button>; })}{filteredModels.length === 0 && <p>没有匹配的模型</p>}</div></div>}<div className="manual-model"><input value={manualModelId} onChange={(event) => setManualModelId(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); addManualModel(); } }} placeholder="手动输入 Model ID"/><button type="button" onClick={addManualModel} disabled={!manualModelId.trim()}><Icon name="plus" size={14}/> 添加</button></div>{profileModels.length > 0 && <div className="selected-models"><p>已选择 {profileModels.length} 个模型</p>{profileModels.map((model) => <div className="selected-model-row" key={model.id}><button type="button" className={`default-model ${model.isDefault ? "active" : ""}`} onClick={() => setDefaultModel(model.id)} title="??????"><span className="radio">{model.isDefault && <i/>}</span><b>{model.id}</b>{model.isDefault && <small>??</small>}</button><div className="model-reasoning-levels" title="??????????????????????????"><span>??</span>{reasoningLevels.map((level) => <button type="button" className={model.reasoningLevels.includes(level) ? "active" : ""} onClick={() => toggleReasoningLevel(model.id, level)} key={level}>{level}</button>)}</div><button type="button" className="remove-model" onClick={() => toggleModel(model)} title="????"><Icon name="close" size={13}/></button></div>)}</div>}<details><summary>高级设置 · 自定义 Headers</summary><label>非敏感 Headers（JSON）<input value={headers} onChange={(event) => setHeaders(event.target.value)} placeholder='{"X-Workspace":"lab"}'/><small className="field-help">Authorization、Cookie、Token 等敏感 Header 会被拒绝。</small></label></details></section><div className="secret-note"><Icon name="shield" size={17}/><div><b>同一配置只保存一份密钥</b><span>所有已选模型共用该连接；API Key 仅写入 Windows Credential Manager。全局用量请从左侧“用量统计”查看。</span></div></div>{feedback && <div className={`feedback ${feedback.kind}`}>{feedback.kind === "ok" && <Icon name="check" size={16}/>}<span>{feedback.text}</span></div>}<footer className="modal-actions">{id && <button type="button" className="danger" onClick={() => void remove()}>删除配置</button>}<span/>{id && <button type="button" onClick={() => void test()}>测试连接</button>}<button className="primary" disabled={saving || profileModels.length === 0}>{saving ? "保存中…" : "保存配置"}</button></footer></form></div></section></div>;
}
