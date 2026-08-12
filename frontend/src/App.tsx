import { FormEvent, ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { eventsOn } from "./lib/wailsRuntime";

type Project = { id: string; name: string; description: string };
type Conversation = { id: string; projectId: string; title: string };
type MessagePart = { type: string; text?: string };
type Message = { id: string; runId?: string; role: "user" | "assistant" | "system" | "tool"; status: string; parts: MessagePart[] };
type Profile = { id: string; name: string; baseUrl: string; modelId: string; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; customHeaders: Record<string, string>; enabled: boolean; isDefault: boolean };
type AvailableModel = { id: string; ownedBy?: string };
type Run = { id: string; status: string; errorMessage?: string; inputTokens: number; outputTokens: number };
type Envelope = { aggregateId: string; type: string; payload: Record<string, unknown> };
type CreateDialog = { kind: "project" | "conversation"; title: string; description: string } | null;

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

function Icon({ name, size = 18 }: { name: "spark" | "plus" | "chat" | "settings" | "shield" | "model" | "send" | "stop" | "search" | "refresh" | "folder" | "check" | "close"; size?: number }) {
  const paths: Record<typeof name, ReactNode> = {
    spark: <><path d="m12 2 1.35 4.15L17.5 7.5l-4.15 1.35L12 13l-1.35-4.15L6.5 7.5l4.15-1.35L12 2Z"/><path d="m5 14 .8 2.2L8 17l-2.2.8L5 20l-.8-2.2L2 17l2.2-.8L5 14Z"/></>,
    plus: <><path d="M12 5v14M5 12h14"/></>, chat: <path d="M21 15a4 4 0 0 1-4 4H8l-5 3V7a4 4 0 0 1 4-4h10a4 4 0 0 1 4 4v8Z"/>,
    settings: <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.09A1.7 1.7 0 0 0 9 19.36a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.63 15 1.7 1.7 0 0 0 3.08 14H3v-4h.09A1.7 1.7 0 0 0 4.64 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 9 4.63h.01A1.7 1.7 0 0 0 10 3.08V3h4v.09A1.7 1.7 0 0 0 15 4.64a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.37 9v.01A1.7 1.7 0 0 0 20.92 10H21v4h-.09A1.7 1.7 0 0 0 19.4 15Z"/></>,
    shield: <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/>, model: <><rect x="3" y="3" width="18" height="18" rx="5"/><path d="M8 9h8M8 12h5M8 15h7"/></>,
    send: <><path d="m22 2-7 20-4-9-9-4 20-7Z"/><path d="M22 2 11 13"/></>, stop: <rect x="6" y="6" width="12" height="12" rx="2"/>, search: <><circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/></>,
    refresh: <><path d="M20 11a8 8 0 1 0-2.34 5.66"/><path d="M20 4v7h-7"/></>, folder: <path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6Z"/>,
    check: <path d="m5 12 4 4L19 6"/>, close: <><path d="m6 6 12 12M18 6 6 18"/></>,
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
  const [activeRun, setActiveRun] = useState<Run | null>(null);
  const [input, setInput] = useState("");
  const [notice, setNotice] = useState("");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [createDialog, setCreateDialog] = useState<CreateDialog>(null);
  const [busy, setBusy] = useState(false);
  const activeRunRef = useRef<Run | null>(null);
  const chatRef = useRef<HTMLElement | null>(null);
  activeRunRef.current = activeRun;

  const loadProjects = useCallback(async () => {
    const values = await backend<Project[]>("ProjectFacade", "ListProjects");
    setProjects(values); setProjectId((current) => current || first(values)?.id || "");
  }, []);
  const loadProfiles = useCallback(async () => {
    const values = await backend<Profile[]>("ModelFacade", "ListModelProfiles");
    setProfiles(values); setProfileId((current) => current || values.find((item) => item.isDefault)?.id || first(values)?.id || "");
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

  useEffect(() => { Promise.all([loadProjects(), loadProfiles()]).catch((error: unknown) => setNotice(errorText(error))); }, [loadProfiles, loadProjects]);
  useEffect(() => { loadConversations(projectId).catch((error: unknown) => setNotice(errorText(error))); }, [loadConversations, projectId]);
  useEffect(() => { loadMessages(conversationId).catch((error: unknown) => setNotice(errorText(error))); }, [conversationId, loadMessages]);
  useEffect(() => { chatRef.current?.scrollTo({ top: chatRef.current.scrollHeight, behavior: "smooth" }); }, [messages]);

  useEffect(() => eventsOn<Envelope>("sciaide:run-event", (event) => {
    const run = activeRunRef.current;
    if (!run || event.aggregateId !== run.id) return;
    if (event.type === "content.delta") {
      const messageId = String(event.payload.messageId ?? ""); const delta = String(event.payload.delta ?? "");
      setMessages((current) => current.map((message) => message.id === messageId ? { ...message, parts: [{ type: "text", text: textOf(message) + delta }] } : message));
    }
    if (event.type.startsWith("run.") && event.payload.run) setActiveRun(event.payload.run as Run);
    if (["run.completed", "run.failed", "run.cancelled"].includes(event.type)) { setBusy(false); loadMessages(conversationId).catch(() => undefined); }
  }), [conversationId, loadMessages]);

  useEffect(() => {
    if (!activeRun || ["completed", "failed", "cancelled", "interrupted"].includes(activeRun.status)) return;
    const timer = window.setInterval(() => backend<{ run: Run; messages: Message[] }>("ChatFacade", "GetRunSnapshot", activeRun.id).then((snapshot) => {
      setActiveRun(snapshot.run); setMessages((current) => snapshot.messages.map((message) => {
        const live = current.find((item) => item.id === message.id);
        return live && textOf(live).length > textOf(message).length ? live : message;
      }));
      if (["completed", "failed", "cancelled", "interrupted"].includes(snapshot.run.status)) setBusy(false);
    }).catch(() => undefined), 700);
    return () => window.clearInterval(timer);
  }, [activeRun?.id, activeRun?.status]);

  async function submitCreate(event: FormEvent) {
    event.preventDefault(); if (!createDialog?.title.trim()) return;
    try {
      if (createDialog.kind === "project") {
        const created = await backend<Project>("ProjectFacade", "CreateProject", { name: createDialog.title.trim(), description: createDialog.description.trim() });
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
    if (!text || !conversationId || !profileId || busy) return;
    setNotice(""); setInput(""); setBusy(true);
    try {
      const run = await backend<Run>("ChatFacade", "StartChat", { conversationId, modelProfileId: profileId, text });
      setActiveRun(run); await loadMessages(conversationId);
    } catch (error) { setBusy(false); setNotice(errorText(error)); }
  }

  const selectedProject = projects.find((item) => item.id === projectId);
  const selectedConversation = conversations.find((item) => item.id === conversationId);
  const selectedProfile = profiles.find((item) => item.id === profileId);
  const usage = useMemo(() => activeRun ? `${activeRun.inputTokens} 输入 · ${activeRun.outputTokens} 输出 tokens` : "", [activeRun]);

  return <div className="app-shell">
    <aside className="sidebar">
      <div className="logo"><span><Icon name="spark" size={21}/></span><div><strong>SciAide</strong><small>Research Copilot</small></div></div>
      <button className="new-project" onClick={() => setCreateDialog({ kind: "project", title: "", description: "" })}><Icon name="plus"/> 新建科研项目</button>
      <div className="project-block"><label className="field-label" htmlFor="project">WORKSPACE</label><div className="select-shell"><Icon name="folder" size={16}/><select id="project" value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="">选择项目</option>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></div></div>
      <div className="section-title"><span>研究会话</span><button aria-label="新建会话" onClick={() => setCreateDialog({ kind: "conversation", title: "", description: "" })} disabled={!projectId}><Icon name="plus" size={17}/></button></div>
      <nav className="conversation-list">{conversations.length ? conversations.map((conversation) => <button className={conversation.id === conversationId ? "active" : ""} key={conversation.id} onClick={() => setConversationId(conversation.id)}><Icon name="chat" size={16}/><span>{conversation.title}</span></button>) : <p className="sidebar-empty">{projectId ? "还没有会话，点击右上角 ＋ 创建" : "选择项目后显示会话"}</p>}</nav>
      <div className="sidebar-footer"><button onClick={() => setSettingsOpen(true)}><span className="nav-icon"><Icon name="settings" size={17}/></span><span><b>模型与 API</b><small>{profiles.length ? `${profiles.length} 个配置可用` : "配置你的第一个模型"}</small></span><span className={selectedProfile?.secretConfigured ? "status-dot ready" : "status-dot"}/></button><div className="local-note"><Icon name="shield" size={13}/> 密钥由系统凭据库保护</div></div>
    </aside>

    <main className="workspace">
      <header className="topbar"><div className="breadcrumbs"><span>{selectedProject?.name ?? "Workspace"}</span><i>/</i><strong>{selectedConversation?.title ?? "新研究"}</strong></div><div className="top-actions"><span className="local-badge"><Icon name="shield" size={13}/> 本地记录</span><div className="model-picker"><span className={selectedProfile?.secretConfigured ? "status-dot ready" : "status-dot"}/><select aria-label="选择模型" value={profileId} onChange={(event) => setProfileId(event.target.value)}><option value="">选择模型</option>{profiles.filter((item) => item.enabled).map((profile) => <option key={profile.id} value={profile.id}>{profile.name} · {profile.modelId}</option>)}</select></div></div></header>
      <section className="chat" aria-live="polite" ref={chatRef}>
        {messages.length === 0 ? <EmptyState hasProject={Boolean(projectId)} hasConversation={Boolean(conversationId)} hasProfile={Boolean(profileId)} openSettings={() => setSettingsOpen(true)} createConversation={() => setCreateDialog({ kind: "conversation", title: "", description: "" })} setPrompt={setInput}/> : <div className="message-stack">{messages.map((message) => <article key={message.id} className={`message ${message.role}`}><div className="avatar">{message.role === "user" ? "你" : <Icon name="spark" size={17}/>}</div><div className="message-body"><div className="message-meta"><b>{message.role === "user" ? "你" : selectedProfile?.name ?? "SciAide"}</b>{message.status === "incomplete" && <span>生成已中断</span>}</div><div className="bubble">{textOf(message) || (message.status === "streaming" ? <span className="typing"><i/><i/><i/> 正在生成回答</span> : "")}</div></div></article>)}</div>}
      </section>
      <footer className="composer-wrap">{notice && <div className="notice"><Icon name="shield" size={15}/>{notice}<button onClick={() => setNotice("")}><Icon name="close" size={14}/></button></div>}{activeRun?.errorMessage && <div className="notice error">{activeRun.errorMessage}</div>}<form className="composer" onSubmit={(event) => void send(event)}><textarea value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} placeholder={conversationId ? "向 SciAide 描述你的研究问题…" : "请先创建或选择研究会话"} disabled={!conversationId || !profileId || busy}/><div className="composer-actions"><span>{usage || <><kbd>Enter</kbd> 发送 · <kbd>Shift Enter</kbd> 换行</>}</span>{busy ? <button type="button" className="stop" onClick={() => activeRun && void backend<void>("ChatFacade", "CancelRun", activeRun.id)}><Icon name="stop" size={15}/> 停止</button> : <button className="send" aria-label="发送" disabled={!input.trim() || !conversationId || !profileId}><Icon name="send" size={17}/></button>}</div></form><p className="composer-hint">AI 可能会出错，重要科研结论请核验原始来源。</p></footer>
    </main>
    {settingsOpen && <ModelSettings profiles={profiles} close={() => setSettingsOpen(false)} refresh={loadProfiles} select={setProfileId}/>}
    {createDialog && <CreateModal value={createDialog} setValue={setCreateDialog} close={() => setCreateDialog(null)} submit={submitCreate}/>}
  </div>;
}

function EmptyState({ hasProject, hasConversation, hasProfile, openSettings, createConversation, setPrompt }: { hasProject: boolean; hasConversation: boolean; hasProfile: boolean; openSettings: () => void; createConversation: () => void; setPrompt: (value: string) => void }) {
  const ready = hasProject && hasConversation && hasProfile;
  const action = !hasProfile ? openSettings : !hasConversation && hasProject ? createConversation : undefined;
  const prompts: Array<[string, string]> = [["研究假设", "帮我把当前研究问题拆成可检验的假设"], ["文献思路", "为这个研究主题梳理关键词和检索策略"], ["实验设计", "设计一套包含对照组的实验方案"]];
  return <div className="empty"><div className="ambient a"/><div className="ambient b"/><div className="ai-mark"><span/><Icon name="spark" size={32}/></div><div className="phase-pill"><i/> SCIENTIFIC AI WORKSPACE</div><h1>{ready ? "今天想探索什么？" : "构建你的科研工作空间"}</h1><p>{!hasProject ? "从左侧新建科研项目，SciAide 会把会话和运行记录组织在项目中。" : !hasConversation ? "创建一个研究会话，让问题、回答与后续产物保持连续。" : !hasProfile ? "连接你的模型 API。密钥只保存在系统凭据库中，不进入数据库。" : "从选题、文献思路到实验设计，把复杂问题拆成清晰的下一步。"}</p>{action && <button className="empty-action" onClick={action}>{!hasProfile ? <Icon name="model"/> : <Icon name="plus"/>}{!hasProfile ? "配置模型" : "创建研究会话"}</button>}{ready && <div className="prompt-grid">{prompts.map(([title,prompt]) => <button key={title} onClick={() => setPrompt(prompt)}><span><Icon name="spark" size={15}/></span><div><b>{title}</b><small>{prompt}</small></div><i>↗</i></button>)}</div>}</div>;
}

function CreateModal({ value, setValue, close, submit }: { value: Exclude<CreateDialog, null>; setValue: (value: CreateDialog) => void; close: () => void; submit: (event: FormEvent) => void }) {
  const project = value.kind === "project";
  return <div className="modal-backdrop compact"><form className="create-modal" onSubmit={submit}><header><span className="dialog-icon"><Icon name={project ? "folder" : "chat"}/></span><div><h2>{project ? "新建科研项目" : "新建研究会话"}</h2><p>{project ? "集中管理一个研究方向下的会话与产物" : "围绕一个明确问题开始连续探索"}</p></div><button type="button" className="close" onClick={close}><Icon name="close"/></button></header><label>{project ? "项目名称" : "会话标题"}<input autoFocus value={value.title} onChange={(event) => setValue({ ...value, title: event.target.value })} placeholder={project ? "例如：单细胞转录组研究" : "例如：梳理实验假设"} maxLength={120} required/></label>{project && <label>简要说明 <span>可选</span><textarea value={value.description} onChange={(event) => setValue({ ...value, description: event.target.value })} placeholder="记录研究目标或背景…" maxLength={500}/></label>}<footer><button type="button" onClick={close}>取消</button><button className="primary">创建</button></footer></form></div>;
}

function ModelSettings({ profiles, close, refresh, select }: { profiles: Profile[]; close: () => void; refresh: () => Promise<void>; select: (id: string) => void }) {
  const [id, setId] = useState(""); const current = profiles.find((item) => item.id === id);
  const [name, setName] = useState(""); const [baseUrl, setBaseUrl] = useState("https://api.openai.com/v1"); const [modelId, setModelId] = useState(""); const [apiKey, setApiKey] = useState(""); const [headers, setHeaders] = useState("");
  const [models, setModels] = useState<AvailableModel[]>([]); const [modelSearch, setModelSearch] = useState(""); const [discovering, setDiscovering] = useState(false); const [feedback, setFeedback] = useState<{ kind: "ok" | "error" | "info"; text: string } | null>(null); const [saving, setSaving] = useState(false);
  useEffect(() => { setName(current?.name ?? ""); setBaseUrl(current?.baseUrl ?? "https://api.openai.com/v1"); setModelId(current?.modelId ?? ""); setApiKey(""); setHeaders(current && Object.keys(current.customHeaders).length ? JSON.stringify(current.customHeaders) : ""); setModels([]); setModelSearch(""); setFeedback(null); }, [current]);
  const filteredModels = useMemo(() => models.filter((item) => item.id.toLowerCase().includes(modelSearch.trim().toLowerCase())), [modelSearch, models]);
  function parsedHeaders() { return headers.trim() ? JSON.parse(headers) as Record<string, string> : {}; }
  async function discover() { setDiscovering(true); setFeedback({ kind: "info", text: "正在读取 /v1/models…" }); try { const values = await backend<AvailableModel[]>("ModelFacade", "DiscoverModels", { profileId: id, baseUrl, apiKey, customHeaders: parsedHeaders() }); setModels(values); setFeedback(values.length ? { kind: "ok", text: `已获取 ${values.length} 个模型，选择一个即可。` } : { kind: "info", text: "服务返回了空列表，请手动填写 Model ID。" }); if (!modelId && values[0]) setModelId(values[0].id); } catch (error) { setModels([]); setFeedback({ kind: "error", text: errorText(error) }); } finally { setDiscovering(false); } }
  async function save(event: FormEvent) { event.preventDefault(); setSaving(true); setFeedback(null); try { const saved = await backend<Profile>("ModelFacade", "SaveModelProfile", { id, name, baseUrl, modelId, apiKey, timeoutSeconds: current?.timeoutSeconds ?? 60, customHeaders: parsedHeaders(), enabled: true, isDefault: profiles.length === 0 || current?.isDefault === true }); await refresh(); setId(saved.id); select(saved.id); setApiKey(""); setFeedback({ kind: "ok", text: "模型配置已安全保存。" }); } catch (error) { setFeedback({ kind: "error", text: errorText(error) }); } finally { setSaving(false); } }
  async function test() { if (!id) return; setFeedback({ kind: "info", text: "正在验证连接…" }); try { await backend<void>("ModelFacade", "TestModelConnection", id); setFeedback({ kind: "ok", text: "连接成功，模型服务可访问。" }); } catch (error) { setFeedback({ kind: "error", text: errorText(error) }); } }
  async function remove() { if (!id || !window.confirm("删除该模型配置及系统凭据？已有运行记录会保留引用。")) return; try { await backend<void>("ModelFacade", "DeleteModelProfile", id); setId(""); await refresh(); setFeedback({ kind: "ok", text: "模型配置已删除。" }); } catch (error) { setFeedback({ kind: "error", text: errorText(error) }); } }
  return <div className="modal-backdrop"><section className="model-modal" role="dialog" aria-modal="true" aria-labelledby="model-title"><header><div><span className="dialog-icon gradient"><Icon name="model"/></span><div><p>MODEL GATEWAY</p><h2 id="model-title">模型与 API</h2></div></div><button className="close" onClick={close}><Icon name="close"/></button></header><div className="settings-grid"><aside><button className={`add-profile ${!id ? "selected" : ""}`} onClick={() => setId("")}><Icon name="plus"/> 添加模型配置</button><div className="profile-caption">已保存</div>{profiles.map((profile) => <button className={`profile-item ${profile.id === id ? "selected" : ""}`} onClick={() => setId(profile.id)} key={profile.id}><span className="provider-logo">AI</span><span><b>{profile.name}</b><small>{profile.modelId}</small></span><i className={profile.secretConfigured ? "status-dot ready" : "status-dot"}/></button>)}</aside><form onSubmit={(event) => void save(event)}><section className="form-section"><div className="form-heading"><span>01</span><div><h3>服务连接</h3><p>兼容 OpenAI API 协议的模型服务</p></div></div><div className="form-row two"><label>配置名称<input value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：实验室模型" required/></label><label>API Key <small>{current?.secretConfigured ? `已保存 ${current.secretMasked}` : "本地服务可留空"}</small><input type="password" autoComplete="new-password" value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder={current?.secretConfigured ? "留空保持现有密钥" : "sk-…"}/></label></div><label>Base URL<div className="endpoint-row"><input value={baseUrl} onChange={(event) => { setBaseUrl(event.target.value); setModels([]); }} placeholder="https://api.openai.com/v1" required/><button type="button" className="discover" onClick={() => void discover()} disabled={discovering || !baseUrl.trim()}><Icon name="refresh" size={15}/>{discovering ? "获取中" : "获取模型"}</button></div><small className="field-help">SciAide 将请求 <code>{baseUrl.replace(/\/$/, "") || "{Base URL}"}/models</code></small></label></section><section className="form-section"><div className="form-heading"><span>02</span><div><h3>选择模型</h3><p>自动获取失败时仍可手动输入 Model ID</p></div></div>{models.length > 0 && <div className="model-browser"><div className="model-search"><Icon name="search" size={16}/><input value={modelSearch} onChange={(event) => setModelSearch(event.target.value)} placeholder={`搜索 ${models.length} 个模型`}/></div><div className="model-results">{filteredModels.slice(0, 80).map((item) => <button type="button" className={item.id === modelId ? "selected" : ""} key={item.id} onClick={() => setModelId(item.id)}><span className="radio">{item.id === modelId && <i/>}</span><b>{item.id}</b><small>{item.ownedBy || "OpenAI-compatible"}</small>{item.id === modelId && <Icon name="check" size={15}/>}</button>)}{filteredModels.length === 0 && <p>没有匹配的模型</p>}</div></div>}<label>Model ID<input value={modelId} onChange={(event) => setModelId(event.target.value)} placeholder="例如：gpt-4.1-mini" required/><small className="field-help">可从上方列表选择，也可直接填写服务商提供的模型标识。</small></label><details><summary>高级设置 · 自定义 Headers</summary><label>非敏感 Headers（JSON）<input value={headers} onChange={(event) => setHeaders(event.target.value)} placeholder='{"X-Workspace":"lab"}'/><small className="field-help">Authorization、Cookie、Token 等敏感 Header 会被拒绝。</small></label></details></section><div className="secret-note"><Icon name="shield" size={17}/><div><b>密钥不会进入项目数据库</b><span>API Key 仅写入 Windows Credential Manager，前端无法读取已保存的明文。</span></div></div>{feedback && <div className={`feedback ${feedback.kind}`}>{feedback.kind === "ok" && <Icon name="check" size={16}/>}<span>{feedback.text}</span></div>}<footer className="modal-actions">{id && <button type="button" className="danger" onClick={() => void remove()}>删除配置</button>}<span/>{id && <button type="button" onClick={() => void test()}>测试连接</button>}<button className="primary" disabled={saving}>{saving ? "保存中…" : "保存配置"}</button></footer></form></div></section></div>;
}
