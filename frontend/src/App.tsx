import { FormEvent, ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { eventsOn } from "./lib/wailsRuntime";

type Project = { id: string; name: string; description: string; workspacePath: string; workspaceKind: "managed" | "external" };
type PermissionMode = "plan" | "full_access";
type Conversation = { id: string; projectId: string; title: string; permissionMode: PermissionMode };
type MessagePart = { type: string; text?: string };
type Message = { id: string; runId?: string; role: "user" | "assistant" | "system" | "tool"; status: string; parts: MessagePart[] };
type ProfileModel = { id: string; ownedBy?: string; enabled: boolean; isDefault: boolean };
type Profile = { id: string; name: string; baseUrl: string; modelId: string; models: ProfileModel[]; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; customHeaders: Record<string, string>; enabled: boolean; isDefault: boolean };
type AvailableModel = { id: string; ownedBy?: string };
type Run = { id: string; conversationId: string; status: string; errorMessage?: string; inputTokens: number; outputTokens: number; permissionMode: PermissionMode };
type PermissionRequirement = { kind: string; resource: string };
type ToolResult = { status: string; text: string; truncated: boolean; meta: { durationMillis?: number; originalBytes?: number } };
type ToolCall = { id: string; runId: string; toolName: string; toolVersion: string; arguments: unknown; status: string; risk: string; permissions: PermissionRequirement[]; errorMessage?: string; result?: ToolResult; createdAt: string; startedAt?: string; completedAt?: string };
type Approval = { id: string; runId: string; toolCallId: string; toolName: string; toolVersion: string; permissionKind: string; resource: string; risk: string; status: string; reason: string };
type RunSnapshot = { run: Run; messages: Message[]; toolCalls: ToolCall[]; pendingApprovals: Approval[] };
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

function Icon({ name, size = 18 }: { name: "spark" | "plus" | "chat" | "settings" | "shield" | "model" | "send" | "stop" | "search" | "refresh" | "folder" | "check" | "close" | "trash" | "tool"; size?: number }) {
  const paths: Record<typeof name, ReactNode> = {
    spark: <><path d="m12 2 1.35 4.15L17.5 7.5l-4.15 1.35L12 13l-1.35-4.15L6.5 7.5l4.15-1.35L12 2Z"/><path d="m5 14 .8 2.2L8 17l-2.2.8L5 20l-.8-2.2L2 17l2.2-.8L5 14Z"/></>,
    plus: <><path d="M12 5v14M5 12h14"/></>, chat: <path d="M21 15a4 4 0 0 1-4 4H8l-5 3V7a4 4 0 0 1 4-4h10a4 4 0 0 1 4 4v8Z"/>,
    settings: <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.09A1.7 1.7 0 0 0 9 19.36a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.63 15 1.7 1.7 0 0 0 3.08 14H3v-4h.09A1.7 1.7 0 0 0 4.64 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 9 4.63h.01A1.7 1.7 0 0 0 10 3.08V3h4v.09A1.7 1.7 0 0 0 15 4.64a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.37 9v.01A1.7 1.7 0 0 0 20.92 10H21v4h-.09A1.7 1.7 0 0 0 19.4 15Z"/></>,
    shield: <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/>, model: <><rect x="3" y="3" width="18" height="18" rx="5"/><path d="M8 9h8M8 12h5M8 15h7"/></>,
    send: <><path d="m22 2-7 20-4-9-9-4 20-7Z"/><path d="M22 2 11 13"/></>, stop: <rect x="6" y="6" width="12" height="12" rx="2"/>, search: <><circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/></>,
    refresh: <><path d="M20 11a8 8 0 1 0-2.34 5.66"/><path d="M20 4v7h-7"/></>, folder: <path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6Z"/>,
    check: <path d="m5 12 4 4L19 6"/>, close: <><path d="m6 6 12 12M18 6 6 18"/></>, trash: <><path d="M3 6h18M8 6V4h8v2M19 6l-1 15H6L5 6M10 11v5M14 11v5"/></>, tool: <><path d="M14.7 6.3a4 4 0 0 0-5 5L3 18l3 3 6.7-6.7a4 4 0 0 0 5-5l-2.2 2.2-3-3 2.2-2.2Z"/></>,
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
    setMessages(await backend<Message[]>("ConversationFacade", "ListMessages", selectedConversation));
  }, []);
  const applySnapshot = useCallback((snapshot: RunSnapshot) => {
    if (snapshot.run.conversationId !== conversationIdRef.current) return;
    setActiveRun(snapshot.run);
    setToolCalls(snapshot.toolCalls ?? []);
    setPendingApprovals(snapshot.pendingApprovals ?? []);
    setMessages((current) => snapshot.messages.map((message) => {
      const live = current.find((item) => item.id === message.id);
      return live && textOf(live).length > textOf(message).length ? live : message;
    }));
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
      const command = { conversationId, modelProfileId: profileId, modelId, text };
      const run = runToSteer ? await backend<Run>("ChatFacade", "SteerChat", runToSteer.id, command) : await backend<Run>("ChatFacade", "StartChat", command);
      setActiveRun(run); await loadMessages(conversationId);
    } catch (error) { setInput((current) => current || text); setBusy(false); setNotice(errorText(error)); }
  }

  const selectedProject = projects.find((item) => item.id === projectId);
  const selectedConversation = conversations.find((item) => item.id === conversationId);
  const selectedProfile = profiles.find((item) => item.id === profileId);
  const selectableModels = useMemo(() => profiles.filter((profile) => profile.enabled).flatMap((profile) => profile.models.filter((model) => model.enabled).map((model) => ({ profile, model }))), [profiles]);
  const selectedModelKey = profileId && modelId ? modelKey(profileId, modelId) : "";
  const usage = useMemo(() => activeRun ? `${activeRun.inputTokens} 输入 · ${activeRun.outputTokens} 输出 tokens` : "", [activeRun]);

  return <div className="app-shell">
    <aside className="sidebar">
      <div className="logo"><span><Icon name="spark" size={21}/></span><div><strong>SciAide</strong><small>Research Copilot</small></div></div>
      <button className="new-project" onClick={() => setCreateDialog({ kind: "project", title: "", description: "", workspacePath: "" })}><Icon name="plus"/> 新建科研项目</button>
      <div className="project-block"><label className="field-label" htmlFor="project">WORKSPACE</label><div className="project-actions"><div className="select-shell"><Icon name="folder" size={16}/><select id="project" value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="">选择项目</option>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></div>{selectedProject && <button className="icon-danger" title="从 SciAide 移除项目" onClick={() => void removeProject(selectedProject)}><Icon name="trash" size={15}/></button>}</div>{selectedProject && <small className="workspace-path" title={selectedProject.workspacePath}>{selectedProject.workspaceKind === "external" ? "外部目录" : "SciAide 托管"} · {selectedProject.workspacePath}</small>}</div>
      <div className="section-title"><span>研究会话</span><button aria-label="新建会话" onClick={() => setCreateDialog({ kind: "conversation", title: "", description: "", workspacePath: "" })} disabled={!projectId}><Icon name="plus" size={17}/></button></div>
      <nav className="conversation-list">{conversations.length ? conversations.map((conversation) => <div className={`conversation-row ${conversation.id === conversationId ? "active" : ""}`} key={conversation.id}><button onClick={() => setConversationId(conversation.id)}><Icon name="chat" size={16}/><span>{conversation.title}</span></button><button className="conversation-remove" title="移除会话" onClick={() => void removeConversation(conversation)}><Icon name="close" size={13}/></button></div>) : <p className="sidebar-empty">{projectId ? "还没有会话，点击右上角 ＋ 创建" : "选择项目后显示会话"}</p>}</nav>
      <div className="sidebar-footer"><button onClick={() => setSettingsOpen(true)}><span className="nav-icon"><Icon name="settings" size={17}/></span><span><b>模型与 API</b><small>{profiles.length ? `${profiles.length} 个配置可用` : "配置你的第一个模型"}</small></span><span className={selectedProfile?.secretConfigured ? "status-dot ready" : "status-dot"}/></button><div className="local-note"><Icon name="shield" size={13}/> 密钥由系统凭据库保护</div></div>
    </aside>

    <main className="workspace">
      <header className="topbar"><div className="breadcrumbs"><span>{selectedProject?.name ?? "Workspace"}</span><i>/</i><strong>{selectedConversation?.title ?? "新研究"}</strong></div><div className="top-actions"><div className="permission-picker" title={busy ? "运行期间不能切换权限模式" : "选择当前研究会话的工具权限模式"}><Icon name="shield" size={13}/><select aria-label="工具权限模式" value={selectedConversation?.permissionMode ?? "plan"} disabled={!selectedConversation || busy} onChange={(event) => void changePermissionMode(event.target.value as PermissionMode)}><option value="plan">Plan · 每次确认</option><option value="full_access">Full Access</option></select></div><div className="model-picker"><span className={selectedProfile?.secretConfigured ? "status-dot ready" : "status-dot"}/><select aria-label="选择模型" value={selectedModelKey} onChange={(event) => { const [nextProfile, nextModel] = splitModelKey(event.target.value); setProfileId(nextProfile); setModelId(nextModel); }}><option value="">选择模型</option>{selectableModels.map(({ profile, model }) => <option key={modelKey(profile.id, model.id)} value={modelKey(profile.id, model.id)}>{profile.name} · {model.id}</option>)}</select></div></div></header>
      <section className="chat" aria-live="polite" ref={chatRef}>
        {messages.length === 0 ? <EmptyState hasProject={Boolean(projectId)} hasConversation={Boolean(conversationId)} hasProfile={Boolean(profileId && modelId)} openSettings={() => setSettingsOpen(true)} createConversation={() => setCreateDialog({ kind: "conversation", title: "", description: "", workspacePath: "" })} setPrompt={setInput}/> : <div className="message-stack">{messages.map((message) => <article key={message.id} className={`message ${message.role}`}><div className="avatar">{message.role === "user" ? "你" : <Icon name="spark" size={17}/>}</div><div className="message-body"><div className="message-meta"><b>{message.role === "user" ? "你" : selectedProfile?.name ?? "SciAide"}</b>{message.status === "incomplete" && <span>生成已中断</span>}</div><div className="bubble">{textOf(message) || (message.status === "streaming" && activeRun?.status !== "waiting_approval" ? <span className="typing"><i/><i/><i/> 正在生成回答</span> : "")}</div>{message.role === "assistant" && message.runId === activeRun?.id && <RunActivity toolCalls={toolCalls} approvals={pendingApprovals} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>}</div></article>)}</div>}
      </section>
      <footer className="composer-wrap">{notice && <div className="notice"><Icon name="shield" size={15}/>{notice}<button onClick={() => setNotice("")}><Icon name="close" size={14}/></button></div>}{activeRun?.errorMessage && <div className="notice error">{activeRun.errorMessage}</div>}<form className="composer" onSubmit={(event) => void send(event)}><textarea value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} placeholder={!conversationId ? "请先创建或选择研究会话" : busy ? "输入新指令可中断当前生成并继续…" : "向 SciAide 描述你的研究问题…"} disabled={!conversationId || !profileId || !modelId}/><div className="composer-actions"><span>{busy ? "发送新消息将中断当前生成并立即继续" : usage || <><kbd>Enter</kbd> 发送 · <kbd>Shift Enter</kbd> 换行</>}</span><div className="composer-buttons">{busy && <button type="button" className="stop" onClick={() => activeRun && void backend<void>("ChatFacade", "CancelRun", activeRun.id).catch((error: unknown) => setNotice(errorText(error)))}><Icon name="stop" size={15}/> 停止</button>}<button className="send" aria-label={busy ? "中断并发送" : "发送"} disabled={!input.trim() || !conversationId || !profileId || !modelId}><Icon name="send" size={17}/></button></div></div></form><p className="composer-hint">AI 可能会出错，重要科研结论请核验原始来源。</p></footer>
    </main>
    {settingsOpen && <ModelSettings profiles={profiles} close={() => setSettingsOpen(false)} refresh={loadProfiles} select={setProfileId}/>}
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

function ModelSettings({ profiles, close, refresh, select }: { profiles: Profile[]; close: () => void; refresh: () => Promise<void>; select: (id: string) => void }) {
  const [id, setId] = useState(""); const current = profiles.find((item) => item.id === id);
  const [name, setName] = useState(""); const [baseUrl, setBaseUrl] = useState("https://api.openai.com/v1"); const [profileModels, setProfileModels] = useState<ProfileModel[]>([]); const [manualModelId, setManualModelId] = useState(""); const [apiKey, setApiKey] = useState(""); const [headers, setHeaders] = useState("");
  const [models, setModels] = useState<AvailableModel[]>([]); const [modelSearch, setModelSearch] = useState(""); const [discovering, setDiscovering] = useState(false); const [feedback, setFeedback] = useState<{ kind: "ok" | "error" | "info"; text: string } | null>(null); const [saving, setSaving] = useState(false);
  useEffect(() => { setName(current?.name ?? ""); setBaseUrl(current?.baseUrl ?? "https://api.openai.com/v1"); setProfileModels(current?.models ?? []); setManualModelId(""); setApiKey(""); setHeaders(current && Object.keys(current.customHeaders).length ? JSON.stringify(current.customHeaders) : ""); setModels([]); setModelSearch(""); setFeedback(null); }, [current]);
  const filteredModels = useMemo(() => models.filter((item) => item.id.toLowerCase().includes(modelSearch.trim().toLowerCase())), [modelSearch, models]);
  function parsedHeaders() { return headers.trim() ? JSON.parse(headers) as Record<string, string> : {}; }
  function toggleModel(item: AvailableModel) { setProfileModels((currentModels) => { const exists = currentModels.some((model) => model.id === item.id); if (exists) { const remaining = currentModels.filter((model) => model.id !== item.id); return remaining.map((model, index) => ({ ...model, isDefault: index === 0 ? !remaining.some((candidate) => candidate.isDefault) : model.isDefault })); } return [...currentModels, { id: item.id, ownedBy: item.ownedBy, enabled: true, isDefault: currentModels.length === 0 }]; }); }
  function addManualModel() { const value = manualModelId.trim(); if (!value || profileModels.some((model) => model.id === value)) return; setProfileModels((currentModels) => [...currentModels, { id: value, enabled: true, isDefault: currentModels.length === 0 }]); setManualModelId(""); }
  function setDefaultModel(modelId: string) { setProfileModels((currentModels) => currentModels.map((model) => ({ ...model, enabled: true, isDefault: model.id === modelId }))); }
  async function discover() { setDiscovering(true); setFeedback({ kind: "info", text: "正在读取 /v1/models…" }); try { const values = await backend<AvailableModel[]>("ModelFacade", "DiscoverModels", { profileId: id, baseUrl, apiKey, customHeaders: parsedHeaders() }); setModels(values); setFeedback(values.length ? { kind: "ok", text: `已获取 ${values.length} 个模型，可勾选多个模型共用该 API Key。` } : { kind: "info", text: "服务返回了空列表，请手动添加 Model ID。" }); } catch (error) { setModels([]); setFeedback({ kind: "error", text: errorText(error) }); } finally { setDiscovering(false); } }
  async function save(event: FormEvent) { event.preventDefault(); const defaultModel = profileModels.find((model) => model.isDefault) ?? profileModels[0]; if (!defaultModel) { setFeedback({ kind: "error", text: "请至少选择或手动添加一个模型。" }); return; } setSaving(true); setFeedback(null); try { const saved = await backend<Profile>("ModelFacade", "SaveModelProfile", { id, name, baseUrl, modelId: defaultModel.id, models: profileModels, apiKey, timeoutSeconds: current?.timeoutSeconds ?? 60, customHeaders: parsedHeaders(), enabled: true, isDefault: profiles.length === 0 || current?.isDefault === true }); await refresh(); setId(saved.id); select(saved.id); setApiKey(""); setFeedback({ kind: "ok", text: `配置和 ${profileModels.length} 个模型已安全保存。` }); } catch (error) { setFeedback({ kind: "error", text: errorText(error) }); } finally { setSaving(false); } }
  async function test() { if (!id) return; setFeedback({ kind: "info", text: "正在验证连接…" }); try { await backend<void>("ModelFacade", "TestModelConnection", id); setFeedback({ kind: "ok", text: "连接成功，模型服务可访问。" }); } catch (error) { setFeedback({ kind: "error", text: errorText(error) }); } }
  async function remove() { if (!id || !window.confirm("删除该 API 配置及系统凭据？若聊天历史仍引用该配置，SciAide 会拒绝删除。")) return; try { await backend<void>("ModelFacade", "DeleteModelProfile", id); setId(""); await refresh(); setFeedback({ kind: "ok", text: "模型配置已删除。" }); } catch (error) { setFeedback({ kind: "error", text: errorText(error) }); } }
  return <div className="modal-backdrop"><section className="model-modal" role="dialog" aria-modal="true" aria-labelledby="model-title"><header><div><span className="dialog-icon gradient"><Icon name="model"/></span><div><p>MODEL GATEWAY</p><h2 id="model-title">模型与 API</h2></div></div><button className="close" onClick={close}><Icon name="close"/></button></header><div className="settings-grid"><aside><button className={`add-profile ${!id ? "selected" : ""}`} onClick={() => setId("")}><Icon name="plus"/> 添加 API 配置</button><div className="profile-caption">已保存</div>{profiles.map((profile) => <button className={`profile-item ${profile.id === id ? "selected" : ""}`} onClick={() => setId(profile.id)} key={profile.id}><span className="provider-logo">AI</span><span><b>{profile.name}</b><small>{profile.models.length} 个模型 · {profile.modelId}</small></span><i className={profile.secretConfigured ? "status-dot ready" : "status-dot"}/></button>)}</aside><form onSubmit={(event) => void save(event)}><section className="form-section"><div className="form-heading"><span>01</span><div><h3>服务连接</h3><p>一个 Base URL 与 API Key 可关联多个模型</p></div></div><div className="form-row two"><label>配置名称<input value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：实验室模型服务" required/></label><label>API Key <small>{current?.secretConfigured ? `已保存 ${current.secretMasked}` : "本地服务可留空"}</small><input type="password" autoComplete="new-password" value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder={current?.secretConfigured ? "留空保持现有密钥" : "sk-…"}/></label></div><label>Base URL<div className="endpoint-row"><input value={baseUrl} onChange={(event) => { setBaseUrl(event.target.value); setModels([]); }} placeholder="https://api.openai.com/v1" required/><button type="button" className="discover" onClick={() => void discover()} disabled={discovering || !baseUrl.trim()}><Icon name="refresh" size={15}/>{discovering ? "获取中" : "获取模型"}</button></div><small className="field-help">SciAide 将请求 <code>{baseUrl.replace(/\/$/, "") || "{Base URL}"}/models</code></small></label></section><section className="form-section"><div className="form-heading"><span>02</span><div><h3>可用模型</h3><p>勾选多个模型，并指定聊天默认模型</p></div></div>{models.length > 0 && <div className="model-browser"><div className="model-search"><Icon name="search" size={16}/><input value={modelSearch} onChange={(event) => setModelSearch(event.target.value)} placeholder={`搜索 ${models.length} 个模型`}/></div><div className="model-results">{filteredModels.slice(0, 80).map((item) => { const selected = profileModels.some((model) => model.id === item.id); return <button type="button" className={selected ? "selected" : ""} key={item.id} onClick={() => toggleModel(item)}><span className="checkbox">{selected && <Icon name="check" size={11}/>}</span><b>{item.id}</b><small>{item.ownedBy || "OpenAI-compatible"}</small>{selected && <span className="chosen">已选</span>}</button>; })}{filteredModels.length === 0 && <p>没有匹配的模型</p>}</div></div>}<div className="manual-model"><input value={manualModelId} onChange={(event) => setManualModelId(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); addManualModel(); } }} placeholder="手动输入 Model ID"/><button type="button" onClick={addManualModel} disabled={!manualModelId.trim()}><Icon name="plus" size={14}/> 添加</button></div>{profileModels.length > 0 && <div className="selected-models"><p>已选择 {profileModels.length} 个模型</p>{profileModels.map((model) => <div key={model.id}><button type="button" className={`default-model ${model.isDefault ? "active" : ""}`} onClick={() => setDefaultModel(model.id)} title="设为默认模型"><span className="radio">{model.isDefault && <i/>}</span><b>{model.id}</b>{model.isDefault && <small>默认</small>}</button><button type="button" className="remove-model" onClick={() => toggleModel(model)} title="移除模型"><Icon name="close" size={13}/></button></div>)}</div>}<details><summary>高级设置 · 自定义 Headers</summary><label>非敏感 Headers（JSON）<input value={headers} onChange={(event) => setHeaders(event.target.value)} placeholder='{"X-Workspace":"lab"}'/><small className="field-help">Authorization、Cookie、Token 等敏感 Header 会被拒绝。</small></label></details></section><div className="secret-note"><Icon name="shield" size={17}/><div><b>同一配置只保存一份密钥</b><span>所有已选模型共用该连接；API Key 仅写入 Windows Credential Manager。</span></div></div>{feedback && <div className={`feedback ${feedback.kind}`}>{feedback.kind === "ok" && <Icon name="check" size={16}/>}<span>{feedback.text}</span></div>}<footer className="modal-actions">{id && <button type="button" className="danger" onClick={() => void remove()}>删除配置</button>}<span/>{id && <button type="button" onClick={() => void test()}>测试连接</button>}<button className="primary" disabled={saving || profileModels.length === 0}>{saving ? "保存中…" : "保存配置"}</button></footer></form></div></section></div>;
}
