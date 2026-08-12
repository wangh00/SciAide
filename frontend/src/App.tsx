import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { EventsOn } from "../wailsjs/runtime/runtime";

type Project = { id: string; name: string; description: string };
type Conversation = { id: string; projectId: string; title: string };
type MessagePart = { type: string; text?: string };
type Message = { id: string; runId?: string; role: "user" | "assistant" | "system" | "tool"; status: string; parts: MessagePart[] };
type Profile = { id: string; name: string; baseUrl: string; modelId: string; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; customHeaders: Record<string, string>; enabled: boolean; isDefault: boolean };
type Run = { id: string; status: string; errorMessage?: string; inputTokens: number; outputTokens: number };
type Envelope = { aggregateId: string; type: string; payload: Record<string, unknown> };

declare global {
  interface Window { go?: { wails?: Record<string, Record<string, (...args: unknown[]) => Promise<unknown>>> } }
}

function backend<T>(facade: string, method: string, ...args: unknown[]): Promise<T> {
  const fn = window.go?.wails?.[facade]?.[method];
  if (!fn) return Promise.reject(new Error("Wails 后端尚未连接，请通过 wails dev 或桌面程序运行。"));
  return fn(...args) as Promise<T>;
}

const textOf = (message: Message) => message.parts.filter((part) => part.type === "text").map((part) => part.text ?? "").join("");
const first = <T,>(items: T[]) => items[0];

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
  const [busy, setBusy] = useState(false);
  const activeRunRef = useRef<Run | null>(null);
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

  useEffect(() => { Promise.all([loadProjects(), loadProfiles()]).catch((error: Error) => setNotice(error.message)); }, [loadProfiles, loadProjects]);
  useEffect(() => { loadConversations(projectId).catch((error: Error) => setNotice(error.message)); }, [loadConversations, projectId]);
  useEffect(() => { loadMessages(conversationId).catch((error: Error) => setNotice(error.message)); }, [conversationId, loadMessages]);

  useEffect(() => {
    return EventsOn("sciaide:run-event", (event: Envelope) => {
      const run = activeRunRef.current;
      if (!run || event.aggregateId !== run.id) return;
      if (event.type === "content.delta") {
        const messageId = String(event.payload.messageId ?? ""); const delta = String(event.payload.delta ?? "");
        setMessages((current) => current.map((message) => message.id === messageId ? { ...message, parts: [{ type: "text", text: textOf(message) + delta }] } : message));
      }
      if (event.type.startsWith("run.") && event.payload.run) setActiveRun(event.payload.run as Run);
      if (["run.completed", "run.failed", "run.cancelled"].includes(event.type)) {
        setBusy(false); loadMessages(conversationId).catch(() => undefined);
      }
    });
  }, [conversationId, loadMessages]);

  useEffect(() => {
    if (!activeRun || ["completed", "failed", "cancelled", "interrupted"].includes(activeRun.status)) return;
    const timer = window.setInterval(() => {
      backend<{ run: Run; messages: Message[] }>("ChatFacade", "GetRunSnapshot", activeRun.id).then((snapshot) => {
        setActiveRun(snapshot.run);
        setMessages((current) => snapshot.messages.map((message) => {
          const live = current.find((item) => item.id === message.id);
          return live && textOf(live).length > textOf(message).length ? live : message;
        }));
        if (["completed", "failed", "cancelled", "interrupted"].includes(snapshot.run.status)) setBusy(false);
      }).catch(() => undefined);
    }, 700);
    return () => window.clearInterval(timer);
  }, [activeRun?.id, activeRun?.status]);

  async function createProject() {
    const name = window.prompt("科研项目名称"); if (!name?.trim()) return;
    const created = await backend<Project>("ProjectFacade", "CreateProject", { name: name.trim(), description: "" });
    await loadProjects(); setProjectId(created.id);
  }
  async function createConversation() {
    if (!projectId) return;
    const title = window.prompt("会话标题", "新研究问题"); if (!title?.trim()) return;
    const created = await backend<Conversation>("ConversationFacade", "CreateConversation", { projectId, title: title.trim() });
    await loadConversations(projectId); setConversationId(created.id);
  }
  async function send(event: FormEvent) {
    event.preventDefault(); const text = input.trim();
    if (!text || !conversationId || !profileId || busy) return;
    setNotice(""); setInput(""); setBusy(true);
    try {
      const run = await backend<Run>("ChatFacade", "StartChat", { conversationId, modelProfileId: profileId, text });
      setActiveRun(run); await loadMessages(conversationId);
    } catch (error) { setBusy(false); setNotice(error instanceof Error ? error.message : String(error)); }
  }
  async function stop() { if (activeRun) await backend<void>("ChatFacade", "CancelRun", activeRun.id); }

  const selectedProject = projects.find((item) => item.id === projectId);
  const selectedConversation = conversations.find((item) => item.id === conversationId);
  const selectedProfile = profiles.find((item) => item.id === profileId);
  const usage = useMemo(() => activeRun ? `${activeRun.inputTokens} ↑ · ${activeRun.outputTokens} ↓ tokens` : "", [activeRun]);

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="logo"><span>SA</span><div><strong>SciAide</strong><small>科研智能体</small></div></div>
        <button className="primary full" onClick={() => void createProject()}>＋ 新建科研项目</button>
        <label className="field-label" htmlFor="project">当前项目</label>
        <select id="project" value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="">请选择项目</option>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select>
        <div className="section-title"><span>研究会话</span><button onClick={() => void createConversation()} disabled={!projectId}>＋</button></div>
        <nav className="conversation-list">{conversations.map((conversation) => <button className={conversation.id === conversationId ? "active" : ""} key={conversation.id} onClick={() => setConversationId(conversation.id)}><span>◌</span><b>{conversation.title}</b></button>)}</nav>
        <div className="sidebar-footer"><button onClick={() => setSettingsOpen(true)}>⚙ 模型与 API 设置</button><p>密钥仅保存在系统凭据库</p></div>
      </aside>

      <main className="workspace">
        <header className="topbar"><div><p>{selectedProject?.name ?? "尚未创建项目"}</p><h1>{selectedConversation?.title ?? "开始你的科研探索"}</h1></div><div className="model-picker"><span className={selectedProfile?.secretConfigured ? "dot online" : "dot"}/><select aria-label="选择模型" value={profileId} onChange={(event) => setProfileId(event.target.value)}><option value="">选择模型</option>{profiles.filter((item) => item.enabled).map((profile) => <option key={profile.id} value={profile.id}>{profile.name} · {profile.modelId}</option>)}</select></div></header>
        <section className="chat" aria-live="polite">
          {messages.length === 0 ? <EmptyState hasProject={Boolean(projectId)} hasConversation={Boolean(conversationId)} hasProfile={Boolean(profileId)} openSettings={() => setSettingsOpen(true)} /> : messages.map((message) => <article key={message.id} className={`message ${message.role}`}><div className="avatar">{message.role === "user" ? "你" : "AI"}</div><div><div className="message-meta">{message.role === "user" ? "你的问题" : selectedProfile?.name ?? "SciAide"}<span>{message.status === "incomplete" ? " · 未完成" : ""}</span></div><div className="bubble">{textOf(message) || (message.status === "streaming" ? <span className="typing">正在思考…</span> : "")}</div></div></article>)}
        </section>
        <footer className="composer-wrap">{notice && <div className="notice">{notice}</div>}{activeRun?.errorMessage && <div className="notice error">{activeRun.errorMessage}</div>}<form className="composer" onSubmit={(event) => void send(event)}><textarea value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} placeholder="描述你的研究问题，Shift + Enter 换行…" disabled={!conversationId || !profileId || busy}/><div className="composer-actions"><span>{usage || "回答与运行记录会保存在本地"}</span>{busy ? <button type="button" className="stop" onClick={() => void stop()}>■ 停止</button> : <button className="send" disabled={!input.trim() || !conversationId || !profileId}>发送 ↑</button>}</div></form></footer>
      </main>
      {settingsOpen && <ModelSettings profiles={profiles} close={() => setSettingsOpen(false)} refresh={loadProfiles} select={setProfileId}/>}
    </div>
  );
}

function EmptyState({ hasProject, hasConversation, hasProfile, openSettings }: { hasProject: boolean; hasConversation: boolean; hasProfile: boolean; openSettings: () => void }) {
  return <div className="empty"><div className="orb">∿</div><p className="eyebrow">P1 · STABLE CHAT</p><h2>把研究问题，交给可靠的对话流</h2><p>{!hasProject ? "先在左侧创建一个科研项目。" : !hasConversation ? "为当前项目新建一个研究会话。" : !hasProfile ? "配置一个 OpenAI-compatible 模型即可开始。" : "可以从论文选题、概念解释、实验设计或数据分析思路开始。"}</p>{!hasProfile && <button className="primary" onClick={openSettings}>配置第一个模型</button>}<div className="prompts"><span>梳理研究假设</span><span>解释科研概念</span><span>设计实验步骤</span></div></div>;
}

function ModelSettings({ profiles, close, refresh, select }: { profiles: Profile[]; close: () => void; refresh: () => Promise<void>; select: (id: string) => void }) {
  const [id, setId] = useState(""); const current = profiles.find((item) => item.id === id);
  const [name, setName] = useState(""); const [baseUrl, setBaseUrl] = useState("https://api.openai.com/v1"); const [modelId, setModelId] = useState(""); const [apiKey, setApiKey] = useState(""); const [headers, setHeaders] = useState(""); const [feedback, setFeedback] = useState(""); const [saving, setSaving] = useState(false);
  useEffect(() => { setName(current?.name ?? ""); setBaseUrl(current?.baseUrl ?? "https://api.openai.com/v1"); setModelId(current?.modelId ?? ""); setApiKey(""); setHeaders(current && Object.keys(current.customHeaders).length ? JSON.stringify(current.customHeaders) : ""); }, [current]);
  async function save(event: FormEvent) { event.preventDefault(); setSaving(true); setFeedback(""); try { let customHeaders: Record<string, string> = {}; if (headers.trim()) customHeaders = JSON.parse(headers) as Record<string, string>; const saved = await backend<Profile>("ModelFacade", "SaveModelProfile", { id, name, baseUrl, modelId, apiKey, timeoutSeconds: current?.timeoutSeconds ?? 60, customHeaders, enabled: true, isDefault: profiles.length === 0 || current?.isDefault === true }); await refresh(); setId(saved.id); select(saved.id); setApiKey(""); setFeedback("配置已安全保存。"); } catch (error) { setFeedback(error instanceof Error ? error.message : String(error)); } finally { setSaving(false); } }
  async function test() { if (!id) return; setFeedback("正在测试连接…"); try { await backend<void>("ModelFacade", "TestModelConnection", id); setFeedback("连接成功。"); } catch (error) { setFeedback(error instanceof Error ? error.message : String(error)); } }
  async function remove() { if (!id || !window.confirm("删除该模型配置及其系统凭据？历史 Run 仍引用的配置不能删除。")) return; try { await backend<void>("ModelFacade", "DeleteModelProfile", id); setId(""); await refresh(); setFeedback("模型配置已删除。"); } catch (error) { setFeedback(error instanceof Error ? error.message : String(error)); } }
  return <div className="modal-backdrop"><section className="modal" role="dialog" aria-modal="true" aria-labelledby="model-title"><header><div><p className="eyebrow">MODEL PROFILES</p><h2 id="model-title">模型与 API 设置</h2></div><button className="close" onClick={close}>×</button></header><div className="settings-grid"><aside><button className={!id ? "selected" : ""} onClick={() => setId("")}>＋ 添加模型</button>{profiles.map((profile) => <button className={profile.id === id ? "selected" : ""} onClick={() => setId(profile.id)} key={profile.id}><b>{profile.name}</b><small>{profile.modelId} · {profile.secretConfigured ? profile.secretMasked : "未设置 Key"}</small></button>)}</aside><form onSubmit={(event) => void save(event)}><label>配置名称<input value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：实验室 GPT" required/></label><label>Base URL<input value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://api.openai.com/v1" required/></label><label>Model ID<input value={modelId} onChange={(event) => setModelId(event.target.value)} placeholder="gpt-4.1-mini" required/></label><label>API Key <small>{current?.secretConfigured ? `已配置 ${current.secretMasked}；留空保持不变` : "将保存到 Windows Credential Manager"}</small><input type="password" autoComplete="new-password" value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder={current?.secretConfigured ? "留空保持现有密钥" : "sk-…"}/></label><label>非敏感自定义 Headers <small>JSON 对象；Authorization、Cookie、API-Key 会被拒绝</small><input value={headers} onChange={(event) => setHeaders(event.target.value)} placeholder='{"X-Workspace":"lab"}'/></label><p className="security-note">🔒 SciAide 不会把 API Key 写入 SQLite、日志或浏览器存储。</p>{feedback && <p className="feedback">{feedback}</p>}<div className="modal-actions">{id && <button type="button" className="danger" onClick={() => void remove()}>删除</button>}{id && <button type="button" onClick={() => void test()}>测试连接</button>}<button className="primary" disabled={saving}>{saving ? "保存中…" : "保存配置"}</button></div></form></div></section></div>;
}
