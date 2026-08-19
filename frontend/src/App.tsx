import { CSSProperties, FormEvent, ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { eventsOn, minimiseWindow, onFileDrop, quitApplication, toggleMaximiseWindow } from "./lib/wailsRuntime";

type Project = { id: string; name: string; description: string; workspacePath: string; workspaceKind: "managed" | "external" };
type PermissionMode = "plan" | "full_access";
type ReasoningLevel = "low" | "medium" | "high" | "xhigh" | "max";
type APIProtocol = "openai_chat_completions" | "openai_responses" | "anthropic_messages";
type Conversation = { id: string; projectId: string; title: string; modelProfileId: string; modelId: string; permissionMode: PermissionMode; reasoningLevel: ReasoningLevel };
type AttachmentReference = { attachmentId: string; originalName: string; mimeType: string; format: string; sizeBytes: number; unitCount: number; truncated: boolean };
type MessagePart = { type: string; text?: string; payload?: AttachmentReference };
type Citation = { id: string; messageId: string; runId: string; toolCallId: string; projectId: string; reference: string; ordinal: number; indexVersionId: string; documentId: string; attachmentId: string; chunkId: string; sourceName: string; mimeType?: string; locator: string; title?: string; quote: string; quoteSha256: string; sourceStart: number; sourceEnd: number; createdAt: string };
type Message = { id: string; runId?: string; role: "user" | "assistant" | "system" | "tool"; status: string; parts: MessagePart[]; citations?: Citation[] };
type Attachment = { id: string; projectId: string; originalName: string; mimeType: string; format: string; sizeBytes: number; sha256: string; status: "parsing" | "ready" | "failed"; unitCount: number; extractedRunes: number; truncated: boolean; errorMessage?: string };
type AttachmentImportBatch = { attachments: Attachment[]; errors: { path: string; message: string }[] };
type KnowledgeDocument = { id: string; projectId: string; attachmentId: string; indexVersionId: string; title: string; attachmentSha256: string; status: "pending" | "indexing" | "ready" | "failed"; parserSchemaVersion: number; chunkingVersion: string; chunkCount: number; errorMessage?: string; createdAt: string; indexedAt?: string; updatedAt: string };
type EmbeddingConfig = { enabled: boolean; baseUrl: string; modelId: string; dimensions: number; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; lastTestedAt?: string; updatedAt: string };
type ProfileModel = { id: string; ownedBy?: string; enabled: boolean; isDefault: boolean; contextWindowTokens: number; autoCompactTokenLimit: number; contextWindowSource: "fallback" | "provider" | "manual" | "builtin"; reasoningLevels: ReasoningLevel[]; reasoningCapabilitySource?: string; reasoningVerifiedLevels?: ReasoningLevel[]; reasoningRejectedLevels?: ReasoningLevel[]; reasoningControlUnsupported?: boolean; reasoningLastRequestedLevel?: ReasoningLevel; reasoningLastResolvedLevel?: ReasoningLevel; reasoningWireMode?: string };
type Profile = { id: string; name: string; apiProtocol: APIProtocol; baseUrl: string; modelId: string; models: ProfileModel[]; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; customHeaders: Record<string, string>; enabled: boolean; isDefault: boolean };
type AvailableModel = { id: string; ownedBy?: string; contextWindowTokens?: number; autoCompactTokenLimit?: number; contextWindowSource?: "fallback" | "provider" | "manual" | "builtin"; reasoningLevels?: ReasoningLevel[]; reasoningCapabilitySource?: string };
type Run = { id: string; conversationId: string; modelProfileId: string; modelId: string; status: string; errorCode?: string; errorMessage?: string; errorDetails?: string; apiProtocol: APIProtocol; inputTokens: number; freshInputTokens: number; outputTokens: number; reasoningTokens: number; reasoningObserved: boolean; reasoningSignatureObserved: boolean; cachedInputTokens: number; cacheWriteTokens: number; cacheReportedTurns: number; cacheHitTurns: number; permissionMode: PermissionMode; requestedReasoningLevel: ReasoningLevel; resolvedReasoningLevel?: ReasoningLevel; contextWindowTokens: number; contextBudgetTokens: number; autoCompactTokenLimit: number; contextWindowSource: "fallback" | "provider" | "manual" | "builtin"; contextCompacted: boolean };
type UsageQuery = { startDate?: string; endDate?: string; modelProfileId?: string; modelId?: string };
type UsageSummary = { runCount: number; modelTurns: number; freshInputTokens: number; outputTokens: number; reasoningTokens: number; cacheReadTokens: number; cacheCreationTokens: number; realTotalTokens: number; cacheReportedTurns: number; cacheHitTurns: number; cacheHitRate: number; cacheDataAvailable: boolean };
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
type SkillActivation = { mode: "explicit" | "suggest"; triggers: string[] };
type SkillManifest = { schemaVersion: number; id: string; name: string; version: string; description: string; entry: string; activation: SkillActivation; requires: { tools: string[]; optionalTools: string[] }; permissions: string[]; compatibility: { sciaide: string }; context: { maxTokens: number } };
type SkillSource = { kind?: "folder" | "zip" | "builtin"; name?: string; hash?: string; archived: boolean };
type InstalledSkill = { manifest: SkillManifest; manifestHash: string; contentHash: string; packageHash: string; integrity: "valid" | "invalid" | "missing"; integrityError?: string; availability: "available" | "unavailable"; availabilityReason?: string; missingRequiredTools: string[]; missingOptionalTools: string[]; source: SkillSource; installedAt: string; updatedAt: string };
type ProjectSkillView = { projectId: string; skillId: string; version: string; enabled: boolean; priority: number; createdAt: string; updatedAt: string; skill: InstalledSkill };
type SkillRefreshResult = { discovered: number; valid: number; invalid: number; missing: number; diagnostics: { message: string }[] };
type SkillInstallResult = { skill: InstalledSkill; replaced: boolean; idempotent: boolean };
type SkillUninstallResult = { skillId: string; version: string; removedProjectLinks: number; recoverable: boolean };
type SkillRollbackResult = { fromVersion: string; toVersion: string; selection: ProjectSkillView };
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
const attachmentsOf = (message: Message) => message.parts.filter((part) => part.type === "media" && part.payload?.attachmentId).map((part) => part.payload as AttachmentReference);
const fileSize = (bytes: number) => bytes < 1024 ? `${bytes} B` : bytes < 1024 * 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
const first = <T,>(items: T[]) => items[0];
const modelKey = (profileId: string, modelId: string) => `${profileId}\t${modelId}`;
const splitModelKey = (value: string): [string, string] => { const index = value.indexOf("\t"); return index < 0 ? ["", ""] : [value.slice(0, index), value.slice(index + 1)]; };
const reasoningLevels: ReasoningLevel[] = ["low", "medium", "high", "xhigh", "max"];
const defaultContextWindowTokens = 200_000;
const automaticCompactLimit = (windowTokens: number) => Math.floor(windowTokens * 0.9);
const protocolLabels: Record<APIProtocol, string> = {
  openai_chat_completions: "OpenAI Chat Completions",
  openai_responses: "OpenAI Responses",
  anthropic_messages: "Anthropic Messages",
};
const inferredReasoningLevels = (protocol: APIProtocol, modelId: string): ReasoningLevel[] => {
  const id = modelId.trim().toLowerCase();
  if (!id) return [];
  if (protocol === "anthropic_messages") {
    if (id.includes("claude-3-7") || id.includes("claude-3.7") || id.includes("claude-4") || id.includes("claude-opus-4") || id.includes("claude-sonnet-4") || id.includes("claude-haiku-4") || !id.includes("claude-")) return reasoningLevels;
    return [];
  }
  if (id.includes("deepseek-reasoner") || id.includes("deepseek-r1") || (id.includes("kimi") && id.includes("thinking"))) return [];
  if (["embedding", "whisper", "tts", "dall-e", "gpt-3.5", "gpt-4o", "gpt-4.1"].some((value) => id.includes(value))) return [];
  if (id.startsWith("o1")) return ["medium", "high"];
  if (id.startsWith("o3") || id.startsWith("o4")) return ["low", "medium", "high"];
  if (id.includes("gpt-5")) {
    if (id.includes("-chat")) return ["medium"];
    if (id.includes("-pro") && !["5.2", "5.3", "5.4"].some((value) => id.includes(value))) return ["high"];
    return ["5.2", "5.3", "5.4", "codex-max"].some((value) => id.includes(value)) ? ["low", "medium", "high", "xhigh"] : ["low", "medium", "high"];
  }
  if (id.includes("grok-3-mini")) return ["low", "high"];
  if (id.includes("deepseek-v4")) return ["low", "medium", "high", "max"];
  return reasoningLevels;
};
const resolvedReasoningLevel = (requested: ReasoningLevel, supported: ReasoningLevel[]): ReasoningLevel | undefined => [...reasoningLevels].reverse().find((item) => reasoningLevels.indexOf(item) <= reasoningLevels.indexOf(requested) && supported.includes(item)) ?? supported[0];
const reasoningDisplay = (requested: ReasoningLevel, model: ProfileModel | undefined, run: Run | null, profileId: string, modelId: string) => {
  const matchingRun = run && run.modelProfileId === profileId && run.modelId === modelId && run.requestedReasoningLevel === requested ? run : null;
  if (matchingRun?.resolvedReasoningLevel) {
    const selected = matchingRun.resolvedReasoningLevel === requested ? requested : `${requested} → ${matchingRun.resolvedReasoningLevel}`;
    const observed = matchingRun.reasoningObserved || matchingRun.reasoningTokens > 0;
    return { text: `${selected} · ${observed ? "已验证" : "参数已接受"}`, kind: observed ? "verified" : matchingRun.resolvedReasoningLevel === requested ? "accepted" : "fallback" };
  }
  if (model?.reasoningControlUnsupported) return { text: `${requested} · 模型默认`, kind: "native" };
  if (model?.reasoningVerifiedLevels?.includes(requested)) return { text: `${requested} · 参数已接受`, kind: "accepted" };
  const supported = (model?.reasoningLevels ?? []).filter((level) => !model?.reasoningRejectedLevels?.includes(level));
  if (model?.reasoningWireMode === "provider_default" && supported.length === 0) return { text: `${requested} · 模型默认`, kind: "native" };
  const resolved = resolvedReasoningLevel(requested, supported);
  if (resolved && (model?.reasoningRejectedLevels?.includes(requested) || !model?.reasoningLevels?.includes(requested))) return { text: `${requested} → ${resolved}`, kind: "fallback" };
  if (model && model.reasoningLevels.length === 0) return { text: `${requested} · 模型默认`, kind: "native" };
  return { text: `${requested} · 待验证`, kind: "pending" };
};
const modelReasoningSummary = (model: ProfileModel) => {
  if (model.reasoningControlUnsupported) return { label: "模型原生思考", title: "服务端已明确拒绝可调思考参数，后续请求保持模型原生行为。" };
  if (model.reasoningWireMode === "provider_default" && model.reasoningLevels.every((level) => model.reasoningRejectedLevels?.includes(level))) return { label: "模型原生思考", title: "服务端未接受任何可调档位，后续请求保持模型原生行为。" };
  if (model.reasoningLastRequestedLevel && model.reasoningLastResolvedLevel && model.reasoningLastRequestedLevel !== model.reasoningLastResolvedLevel) {
    return { label: `运行时回退 · ${model.reasoningLastRequestedLevel}→${model.reasoningLastResolvedLevel}`, title: `真实对话验证后，服务端接受的最高相邻档位为 ${model.reasoningLastResolvedLevel}。` };
  }
  if (model.reasoningVerifiedLevels?.length) return { label: `参数已接受 · ${model.reasoningVerifiedLevels.join("/")}`, title: "这些档位已在真实对话请求中被服务端接受；只有观察到 thinking/reasoning 块或 reasoning token 才标记为已验证。" };
  if (model.reasoningCapabilitySource === "provider") return { label: `服务端声明 · ${model.reasoningLevels.join("/")}`, title: "档位来自 /v1/models 返回的能力元数据，仍会在真实请求被明确拒绝时安全回退。" };
  if (model.reasoningCapabilitySource === "manual") return { label: `手动配置 · ${model.reasoningLevels.join("/")}`, title: "档位由用户显式配置，真实请求被明确拒绝时会安全回退。" };
  if (model.reasoningLevels.length) return { label: `待运行验证 · ${model.reasoningLevels.join("/")}`, title: "不会发送后台探测；下一次真实对话将从所选档位开始验证。" };
  return { label: "模型原生思考", title: "不发送可调思考参数，保持模型原生行为。" };
};
const modelContextSummary = (model: ProfileModel) => {
  const source = model.contextWindowSource === "provider" ? "服务声明" : model.contextWindowSource === "manual" ? "手动" : model.contextWindowSource === "builtin" ? "内置目录" : "默认";
  return `${source} · ${(model.contextWindowTokens / 1000).toLocaleString(undefined, { maximumFractionDigits: 1 })}K`;
};
const messageRoleRank = (role: Message["role"]) => role === "user" ? 0 : role === "assistant" ? 1 : 2;
const orderedMessages = (values: Message[]) => values.map((message, index) => ({ message, index })).sort((left, right) => {
  if (left.message.runId && left.message.runId === right.message.runId) {
    const rank = messageRoleRank(left.message.role) - messageRoleRank(right.message.role);
    if (rank !== 0) return rank;
  }
  return left.index - right.index;
}).map(({ message }) => message);

function Icon({ name, size = 18 }: { name: "spark" | "plus" | "chat" | "settings" | "shield" | "model" | "send" | "stop" | "search" | "refresh" | "folder" | "check" | "close" | "trash" | "tool" | "server" | "chart" | "skill" | "paperclip" | "library"; size?: number }) {
  const paths: Record<typeof name, ReactNode> = {
    spark: <><path d="m12 2 1.35 4.15L17.5 7.5l-4.15 1.35L12 13l-1.35-4.15L6.5 7.5l4.15-1.35L12 2Z"/><path d="m5 14 .8 2.2L8 17l-2.2.8L5 20l-.8-2.2L2 17l2.2-.8L5 14Z"/></>,
    plus: <><path d="M12 5v14M5 12h14"/></>, chat: <path d="M21 15a4 4 0 0 1-4 4H8l-5 3V7a4 4 0 0 1 4-4h10a4 4 0 0 1 4 4v8Z"/>,
    settings: <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.09A1.7 1.7 0 0 0 9 19.36a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.63 15 1.7 1.7 0 0 0 3.08 14H3v-4h.09A1.7 1.7 0 0 0 4.64 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 9 4.63h.01A1.7 1.7 0 0 0 10 3.08V3h4v.09A1.7 1.7 0 0 0 15 4.64a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.37 9v.01A1.7 1.7 0 0 0 20.92 10H21v4h-.09A1.7 1.7 0 0 0 19.4 15Z"/></>,
    shield: <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/>, model: <><rect x="3" y="3" width="18" height="18" rx="5"/><path d="M8 9h8M8 12h5M8 15h7"/></>,
    send: <><path d="m22 2-7 20-4-9-9-4 20-7Z"/><path d="M22 2 11 13"/></>, stop: <rect x="6" y="6" width="12" height="12" rx="2"/>, search: <><circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/></>,
    refresh: <><path d="M20 11a8 8 0 1 0-2.34 5.66"/><path d="M20 4v7h-7"/></>, folder: <path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6Z"/>,
    check: <path d="m5 12 4 4L19 6"/>, close: <><path d="m6 6 12 12M18 6 6 18"/></>, trash: <><path d="M3 6h18M8 6V4h8v2M19 6l-1 15H6L5 6M10 11v5M14 11v5"/></>, tool: <><path d="M14.7 6.3a4 4 0 0 0-5 5L3 18l3 3 6.7-6.7a4 4 0 0 0 5-5l-2.2 2.2-3-3 2.2-2.2Z"/></>, server: <><rect x="4" y="3" width="16" height="7" rx="2"/><rect x="4" y="14" width="16" height="7" rx="2"/><path d="M8 6.5h.01M8 17.5h.01M12 6.5h5M12 17.5h5"/></>,
    chart: <><path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/></>,
    skill: <><path d="M7 3h8l4 4v14H7a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Z"/><path d="M14 3v5h5M9 12h6M9 16h6"/></>,
    paperclip: <path d="m21.4 11.6-8.9 8.9a6 6 0 0 1-8.5-8.5l9.6-9.6a4 4 0 0 1 5.7 5.7l-9.6 9.6a2 2 0 1 1-2.8-2.8l8.9-8.9"/>,
    library: <><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2Z"/><path d="M8 7h8M8 11h6"/></>,
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
  const [pendingAttachments, setPendingAttachments] = useState<Attachment[]>([]);
  const [importingAttachments, setImportingAttachments] = useState(false);
  const [notice, setNotice] = useState("");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [mcpOpen, setMcpOpen] = useState(false);
  const [usageOpen, setUsageOpen] = useState(false);
  const [skillsOpen, setSkillsOpen] = useState(false);
  const [knowledgeOpen, setKnowledgeOpen] = useState(false);
  const [createDialog, setCreateDialog] = useState<CreateDialog>(null);
  const [busy, setBusy] = useState(false);
  const activeRunRef = useRef<Run | null>(null);
  const conversationIdRef = useRef("");
  const modelSelectionRef = useRef({ profileId: "", modelId: "" });
  const restoringModelSelectionRef = useRef(false);
  const modelSelectionSaveRef = useRef<Promise<void>>(Promise.resolve());
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
    setActiveRun(null); setToolCalls([]); setPendingApprovals([]); setPendingAttachments([]); setBusy(false);
    if (!conversationId) { setMessages([]); return; }
    Promise.all([
      loadMessages(conversationId),
      backend<RunSnapshot | null>("ChatFacade", "GetLatestRunSnapshot", conversationId).then((snapshot) => { if (snapshot) applySnapshot(snapshot); }),
    ]).catch((error: unknown) => setNotice(errorText(error)));
  }, [applySnapshot, conversationId, loadMessages]);
  useEffect(() => { chatRef.current?.scrollTo({ top: chatRef.current.scrollHeight, behavior: "smooth" }); }, [messages]);
  useEffect(() => onFileDrop((paths) => {
    if (projectId && conversationId && paths.length) void importDroppedDocuments(paths);
  }), [projectId, conversationId, importingAttachments]);

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
    if (["run.completed", "run.failed", "run.cancelled"].includes(event.type)) { setBusy(false); loadMessages(conversationId).catch(() => undefined); loadProfiles().catch(() => undefined); }
  }), [conversationId, loadMessages, loadProfiles]);

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
        const created = await backend<Conversation>("ConversationFacade", "CreateConversation", { projectId, title: createDialog.title.trim(), modelProfileId: profileId, modelId });
        await loadConversations(projectId); setConversationId(created.id);
      }
      setCreateDialog(null);
    } catch (error) { setNotice(errorText(error)); }
  }

  async function send(event: FormEvent) {
    event.preventDefault(); const text = input.trim();
    if ((!text && pendingAttachments.length === 0) || !conversationId || !profileId || !modelId) return;
    const runToSteer = busy && activeRun?.conversationId === conversationId ? activeRun : null;
    const submittedAttachments = pendingAttachments;
    setNotice(""); setInput(""); setPendingAttachments([]); setBusy(true);
    try {
      const command = { conversationId, modelProfileId: profileId, modelId, reasoningLevel: selectedConversation?.reasoningLevel ?? "medium", text, attachmentIds: submittedAttachments.map((item) => item.id) };
      const run = runToSteer ? await backend<Run>("ChatFacade", "SteerChat", runToSteer.id, command) : await backend<Run>("ChatFacade", "StartChat", command);
      setActiveRun(run); const persistedConversation = await backend<Conversation>("ConversationFacade", "GetConversation", conversationId); setConversations((current) => current.map((item) => item.id === persistedConversation.id ? persistedConversation : item)); await loadMessages(conversationId);
    } catch (error) { setInput((current) => current || text); setPendingAttachments((current) => current.length ? current : submittedAttachments); setBusy(false); setNotice(errorText(error)); }
  }

  async function attachDocuments() {
    if (!projectId || importingAttachments) return;
    setImportingAttachments(true); setNotice("");
    try {
      const result = await backend<AttachmentImportBatch>("AttachmentFacade", "ChooseAndImportDocuments", projectId);
      const ready = result.attachments.filter((item) => item.status === "ready");
      setPendingAttachments((current) => [...current, ...ready.filter((item) => !current.some((existing) => existing.id === item.id))].slice(0, 20));
      if (result.errors.length) setNotice(`有 ${result.errors.length} 个文件未能导入：${first(result.errors)?.message ?? "未知错误"}`);
    } catch (error) { setNotice(errorText(error)); }
    finally { setImportingAttachments(false); }
  }

  async function importDroppedDocuments(paths: string[]) {
    if (!projectId || !conversationId || importingAttachments || paths.length === 0) return;
    setImportingAttachments(true); setNotice("");
    try {
      const result = await backend<AttachmentImportBatch>("AttachmentFacade", "ImportDocumentPaths", projectId, paths);
      const ready = result.attachments.filter((item) => item.status === "ready");
      setPendingAttachments((current) => [...current, ...ready.filter((item) => !current.some((existing) => existing.id === item.id))].slice(0, 20));
      if (result.errors.length) setNotice(`有 ${result.errors.length} 个文件未能导入：${first(result.errors)?.message ?? "未知错误"}`);
    } catch (error) { setNotice(errorText(error)); }
    finally { setImportingAttachments(false); }
  }

  const selectedProject = projects.find((item) => item.id === projectId);
  const selectedConversation = conversations.find((item) => item.id === conversationId);
  const selectedProfile = profiles.find((item) => item.id === profileId);
  const selectedModel = selectedProfile?.models.find((item) => item.id === modelId);
  const selectableModels = useMemo(() => profiles.filter((profile) => profile.enabled).flatMap((profile) => profile.models.filter((model) => model.enabled).map((model) => ({ profile, model }))), [profiles]);
  const selectedModelKey = profileId && modelId ? modelKey(profileId, modelId) : "";
  const usage = useMemo(() => activeRun ? `${activeRun.inputTokens} 输入 · ${activeRun.outputTokens} 输出${activeRun.reasoningTokens > 0 ? ` · ${activeRun.reasoningTokens} 推理` : ""}${activeRun.cacheReportedTurns > 0 ? ` · ${activeRun.cachedInputTokens} 缓存命中` : ""} tokens` : "", [activeRun]);
  const reasoning = reasoningDisplay(selectedConversation?.reasoningLevel ?? "medium", selectedModel, activeRun, profileId, modelId);

  useEffect(() => {
    if (!selectedConversation) return;
    const preferred = selectableModels.find(({ profile, model }) => profile.id === selectedConversation.modelProfileId && model.id === selectedConversation.modelId);
    const fallback = selectableModels.find(({ profile, model }) => profile.isDefault && model.isDefault) ?? first(selectableModels);
    const next = preferred ?? fallback;
    if (next && (next.profile.id !== modelSelectionRef.current.profileId || next.model.id !== modelSelectionRef.current.modelId)) {
      restoringModelSelectionRef.current = true;
      setProfileId(next.profile.id);
      setModelId(next.model.id);
    }
  }, [selectableModels, selectedConversation]);

  useEffect(() => {
    if (restoringModelSelectionRef.current) {
      restoringModelSelectionRef.current = false;
      return;
    }
    if (!selectedConversation || !profileId || !modelId || (selectedConversation.modelProfileId === profileId && selectedConversation.modelId === modelId)) return;
    modelSelectionSaveRef.current = modelSelectionSaveRef.current
      .catch(() => undefined)
      .then(() => backend<Conversation>("ConversationFacade", "SetModelSelection", selectedConversation.id, profileId, modelId))
      .then((updated) => { setConversations((current) => current.map((item) => item.id === updated.id ? updated : item)); })
      .catch((error: unknown) => { setNotice(errorText(error)); });
  }, [modelId, profileId, selectedConversation]);

  return <div className="app-shell">
	<div className="window-titlebar"><div className="window-brand"><span><Icon name="spark" size={13}/></span><b>SciAide</b></div><div className="window-controls"><button type="button" aria-label="最小化窗口" title="最小化" onClick={minimiseWindow}>—</button><button type="button" aria-label="最大化或还原窗口" title="最大化/还原" onClick={toggleMaximiseWindow}>□</button><button type="button" className="window-close" aria-label="关闭窗口" title="关闭" onClick={quitApplication}>×</button></div></div>
    <aside className="sidebar">
      <div className="logo"><span><Icon name="spark" size={21}/></span><div><strong>SciAide</strong><small>Research Copilot</small></div></div>
      <button className="new-project" onClick={() => setCreateDialog({ kind: "project", title: "", description: "", workspacePath: "" })}><Icon name="plus"/> 新建科研项目</button>
      <div className="project-block"><label className="field-label" htmlFor="project">WORKSPACE</label><div className="project-actions"><div className="select-shell"><Icon name="folder" size={16}/><select id="project" value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="">选择项目</option>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></div>{selectedProject && <button className="icon-danger" title="从 SciAide 移除项目" onClick={() => void removeProject(selectedProject)}><Icon name="trash" size={15}/></button>}</div>{selectedProject && <small className="workspace-path" title={selectedProject.workspacePath}>{selectedProject.workspaceKind === "external" ? "外部目录" : "SciAide 托管"} · {selectedProject.workspacePath}</small>}</div>
      <div className="section-title"><span>研究会话</span><button aria-label="新建会话" onClick={() => setCreateDialog({ kind: "conversation", title: "", description: "", workspacePath: "" })} disabled={!projectId}><Icon name="plus" size={17}/></button></div>
      <nav className="conversation-list">{conversations.length ? conversations.map((conversation) => <div className={`conversation-row ${conversation.id === conversationId ? "active" : ""}`} key={conversation.id}><button onClick={() => setConversationId(conversation.id)}><Icon name="chat" size={16}/><span>{conversation.title}</span></button><button className="conversation-remove" title="移除会话" onClick={() => void removeConversation(conversation)}><Icon name="close" size={13}/></button></div>) : <p className="sidebar-empty">{projectId ? "还没有会话，点击右上角 ＋ 创建" : "选择项目后显示会话"}</p>}</nav>
      <div className="sidebar-footer"><button onClick={() => setUsageOpen(true)}><span className="nav-icon"><Icon name="chart" size={17}/></span><span><b>用量统计</b><small>全部模型 · 日期与缓存命中</small></span></button><button onClick={() => setSkillsOpen(true)}><span className="nav-icon"><Icon name="skill" size={17}/></span><span><b>Skills</b><small>{selectedProject ? `管理 ${selectedProject.name} 的研究技能` : "安装与管理研究技能"}</small></span></button><button onClick={() => setMcpOpen(true)}><span className="nav-icon"><Icon name="server" size={17}/></span><span><b>MCP Servers</b><small>连接科研工具与数据服务</small></span></button><button onClick={() => setSettingsOpen(true)}><span className="nav-icon"><Icon name="settings" size={17}/></span><span><b>模型与 API</b><small>{profiles.length ? `${profiles.length} 个配置可用` : "配置你的第一个模型"}</small></span><span className={selectedProfile?.secretConfigured ? "status-dot ready" : "status-dot"}/></button><div className="local-note"><Icon name="shield" size={13}/> 密钥由系统凭据库保护</div></div>
    </aside>

    <main className="workspace">
      <header className="topbar"><div className="breadcrumbs"><span>{selectedProject?.name ?? "Workspace"}</span><i>/</i><strong>{selectedConversation?.title ?? "新研究"}</strong></div><div className="top-actions"><button type="button" className="knowledge-open" aria-label="打开项目知识库" title={selectedProject ? `管理 ${selectedProject.name} 的知识库` : "请先选择项目"} disabled={!selectedProject} onClick={() => setKnowledgeOpen(true)}><Icon name="library" size={16}/><span>知识库</span></button><div className="permission-picker" title={busy ? "运行期间不能切换权限模式" : "当前 Workspace 内只读免确认；外部读取、写入和其他工具需确认"}><Icon name="shield" size={13}/><select aria-label="工具权限模式" value={selectedConversation?.permissionMode ?? "plan"} disabled={!selectedConversation || busy} onChange={(event) => void changePermissionMode(event.target.value as PermissionMode)}><option value="plan">Plan · 写入/工具确认</option><option value="full_access">Full Access</option></select></div><div className="model-picker"><span className={selectedProfile?.secretConfigured ? "status-dot ready" : "status-dot"}/><select aria-label="选择模型" value={selectedModelKey} onChange={(event) => { const [nextProfile, nextModel] = splitModelKey(event.target.value); setProfileId(nextProfile); setModelId(nextModel); }}><option value="">选择模型</option>{selectableModels.map(({ profile, model }) => <option key={modelKey(profile.id, model.id)} value={modelKey(profile.id, model.id)}>{profile.name} · {model.id}</option>)}</select></div><div className={`reasoning-picker ${reasoning.kind}`} title="参数已接受只代表服务端接受档位；收到 thinking/reasoning 块或 reasoning token 后才显示已验证。明确拒绝时逐级回退，不发送后台探测。"><Icon name="spark" size={13}/><select aria-label="思考强度" value={selectedConversation?.reasoningLevel ?? "medium"} disabled={!selectedConversation || busy} onChange={(event) => void changeReasoningLevel(event.target.value as ReasoningLevel)}>{reasoningLevels.map((level) => <option value={level} key={level}>{level}</option>)}</select><span className="reasoning-state">{reasoning.text.replace(`${selectedConversation?.reasoningLevel ?? "medium"} · `, "").replace(`${selectedConversation?.reasoningLevel ?? "medium"} `, "")}</span></div></div></header>
      <section className="chat" aria-live="polite" ref={chatRef}>
        {messages.length === 0 ? <EmptyState hasProject={Boolean(projectId)} hasConversation={Boolean(conversationId)} hasProfile={Boolean(profileId && modelId)} openSettings={() => setSettingsOpen(true)} createConversation={() => setCreateDialog({ kind: "conversation", title: "", description: "", workspacePath: "" })} setPrompt={setInput}/> : <div className="message-stack">{messages.map((message) => <article key={message.id} className={`message ${message.role}`}><div className="avatar">{message.role === "user" ? "你" : <Icon name="spark" size={17}/>}</div><div className="message-body"><div className="message-meta"><b>{message.role === "user" ? "你" : selectedProfile?.name ?? "SciAide"}</b>{message.status === "incomplete" && <span>生成已中断</span>}</div>{attachmentsOf(message).length > 0 && <div className="message-attachments">{attachmentsOf(message).map((item) => <div className="attachment-card" key={item.attachmentId}><span><Icon name="skill" size={16}/></span><div><b title={item.originalName}>{item.originalName}</b><small>{item.format.toUpperCase()} · {fileSize(item.sizeBytes)} · {item.unitCount} 个可读单元{item.truncated ? " · 已截断" : ""}</small></div></div>)}</div>}<CitedAnswer message={message} waiting={message.status === "streaming" && activeRun?.status !== "waiting_approval"}/>{message.role === "assistant" && activeRun && message.runId === activeRun.id && <RunActivity run={activeRun} toolCalls={toolCalls} approvals={pendingApprovals} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>}</div></article>)}</div>}
      </section>
      <footer className="composer-wrap">{notice && <div className="notice"><Icon name="shield" size={15}/><span>{notice}</span><button onClick={() => setNotice("")}><Icon name="close" size={14}/></button></div>}{activeRun?.errorMessage && <RunErrorNotice run={activeRun}/>}<form className="composer" onSubmit={(event) => void send(event)}>{pendingAttachments.length > 0 && <div className="pending-attachments">{pendingAttachments.map((item) => <div key={item.id}><span><Icon name="skill" size={15}/></span><b title={item.originalName}>{item.originalName}</b><small>{item.format.toUpperCase()} · {fileSize(item.sizeBytes)}</small><button type="button" aria-label={`移除 ${item.originalName}`} title="移除附件" onClick={() => setPendingAttachments((current) => current.filter((value) => value.id !== item.id))}><Icon name="close" size={13}/></button></div>)}</div>}<textarea value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} placeholder={!conversationId ? "请先创建或选择研究会话" : busy ? "输入新指令可中断当前生成并继续…" : "向 SciAide 描述你的研究问题…"} disabled={!conversationId || !profileId || !modelId}/><div className="composer-actions"><span>{busy ? "发送新消息将中断当前生成并立即继续" : usage || <><kbd>Enter</kbd> 发送 · <kbd>Shift Enter</kbd> 换行</>}</span><div className="composer-buttons"><button type="button" className="attach" aria-label="添加当前对话附件" title="仅添加到当前对话，不会加入项目知识库" disabled={!conversationId || !projectId || importingAttachments} onClick={() => void attachDocuments()}><Icon name={importingAttachments ? "refresh" : "paperclip"} size={17}/></button>{busy && <button type="button" className="stop" onClick={() => activeRun && void backend<void>("ChatFacade", "CancelRun", activeRun.id).catch((error: unknown) => setNotice(errorText(error)))}><Icon name="stop" size={15}/> 停止</button>}<button className="send" aria-label={busy ? "中断并发送" : "发送"} disabled={(!input.trim() && pendingAttachments.length === 0) || !conversationId || !profileId || !modelId}><Icon name="send" size={17}/></button></div></div></form><p className="composer-hint">AI 可能会出错，重要科研结论请核验原始来源。</p></footer>
    </main>
    {settingsOpen && <ModelSettings profiles={profiles} close={() => setSettingsOpen(false)} refresh={loadProfiles} select={setProfileId}/>}
    {mcpOpen && <MCPSettings close={() => setMcpOpen(false)}/>}
    {usageOpen && <UsageDashboard profiles={profiles} close={() => setUsageOpen(false)}/>}
    {skillsOpen && <SkillSettings project={selectedProject} close={() => setSkillsOpen(false)}/>}
    {knowledgeOpen && selectedProject && <KnowledgeLibrary project={selectedProject} close={() => setKnowledgeOpen(false)}/>}
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
      const display = reasoningDisplay(level, selectedModel, null, profileId, modelId);
      setNotice(`思考强度：${display.text}`);
    } catch (error) { setNotice(errorText(error)); }
  }

  async function removeConversation(value: Conversation) {
    if (!window.confirm(`移除研究会话“${value.title}”？\n\n该会话的消息和运行记录将删除，Workspace 文件不受影响。`)) return;
    try { await backend("ConversationFacade", "RemoveConversation", value.id); if (conversationId === value.id) { setConversationId(""); setMessages([]); } await loadConversations(projectId); setNotice("研究会话已移除。"); } catch (error) { setNotice(errorText(error)); }
  }
}

const citationMarkerPattern = /(\[K-[0-9A-F]{12}\])/g;
const exactCitationMarkerPattern = /^\[K-[0-9A-F]{12}\]$/;
const citationHanPattern = /\p{Script=Han}/u;
const citationNoSpaceBeforePattern = /[，。；：！？、）》】〕］”’％‰,.!?;:%)\]}]/u;
const citationNoSpaceAfterPattern = /[（《【〔［“‘([{]/u;

function formatCitationQuote(value: string) {
  const blocks = value.replace(/\r\n?/g, "\n").trim().split(/\n[ \t]*\n+/);
  return blocks.map((block) => {
    const parts = block.split("\n").map((part) => part.trim().replace(/[ \t]+/g, " ")).filter(Boolean);
    return parts.reduce((result, part) => {
      if (!result) return part;
      const previous = result.at(-1) ?? "";
      const next = part[0] ?? "";
      const compact = (citationHanPattern.test(previous) && citationHanPattern.test(next))
        || citationNoSpaceAfterPattern.test(previous)
        || citationNoSpaceBeforePattern.test(next)
        || previous === "-"
        || next === "-";
      return result + (compact ? "" : " ") + part;
    }, "");
  }).filter(Boolean).join("\n\n");
}

function CitedAnswer({ message, waiting }: { message: Message; waiting: boolean }) {
  const [selectedReference, setSelectedReference] = useState("");
  const [showRawQuote, setShowRawQuote] = useState(false);
  useEffect(() => setSelectedReference(""), [message.id]);
  useEffect(() => setShowRawQuote(false), [message.id, selectedReference]);
  const text = textOf(message);
  const citations = [...(message.citations ?? [])].sort((left, right) => left.ordinal - right.ordinal);
  const byReference = new Map(citations.map((value, index) => [value.reference, { value, number: index + 1 }]));
  const selected = byReference.get(selectedReference)?.value;
  const content = message.role !== "assistant" ? text : text.split(citationMarkerPattern).map((part, index) => {
    const citation = byReference.get(part);
    if (citation) return <button type="button" className="citation-marker" key={`${part}-${index}`} title={`${citation.value.sourceName} · ${citation.value.locator}`} onClick={() => setSelectedReference((current) => current === part ? "" : part)}>[{citation.number}]</button>;
    if (exactCitationMarkerPattern.test(part)) {
      return <span className="citation-unverified" title="该引用标记未通过当前 Run 的证据校验" key={`${part}-${index}`}>{part}</span>;
    }
    return part;
  });
  return <>
    <div className="bubble">{text ? content : waiting ? <span className="typing"><i/><i/><i/> 正在生成回答</span> : ""}</div>
    {selected && <section className="citation-detail" aria-label="引用证据">
      <header><span><Icon name="library" size={14}/></span><div><b>{selected.sourceName}</b><small>{selected.locator}{selected.title ? ` · ${selected.title}` : ""}</small></div><button type="button" className="citation-view-toggle" title={showRawQuote ? "恢复整理后的文本" : "查看参与证据校验的原始文本"} onClick={() => setShowRawQuote((value) => !value)}>{showRawQuote ? "整理文本" : "原始文本"}</button><button type="button" aria-label="关闭引用详情" onClick={() => setSelectedReference("")}><Icon name="close" size={13}/></button></header>
      <blockquote>{showRawQuote ? selected.quote : formatCitationQuote(selected.quote)}</blockquote>
      <footer><span>已验证引用</span><code>{selected.reference}</code><small title={selected.quoteSha256}>证据 {selected.quoteSha256.slice(0, 12)}</small></footer>
    </section>}
  </>;
}

function RunErrorNotice({ run }: { run: Run }) {
  const [open, setOpen] = useState(false);
  useEffect(() => setOpen(false), [run.id]);
  return <div className={`notice error run-error ${open ? "expanded" : ""}`}>
    <Icon name="shield" size={15}/><span>{run.errorMessage}</span><button type="button" className="run-error-toggle" onClick={() => setOpen((value) => !value)}>{open ? "收起" : "详情"}</button>
    {open && <div className="run-error-details"><div><code>{run.errorCode || "UNKNOWN_ERROR"}</code><span>{protocolLabels[run.apiProtocol] ?? run.apiProtocol} · {run.modelId}</span></div><pre>{run.errorDetails?.trim() || "该历史请求没有保存服务端错误载荷。请使用当前版本重新发送后查看详情。"}</pre></div>}
  </div>;
}

const toolStatusText: Record<string, string> = {
  pending: "已请求", awaiting_approval: "等待确认", running: "执行中", completed: "已完成",
  failed: "失败", denied: "已拒绝", cancelled: "已取消", interrupted: "已中断",
};

function RunActivity({ run, toolCalls, approvals, resolvingApprovalId, resolveApproval }: { run: Run; toolCalls: ToolCall[]; approvals: Approval[]; resolvingApprovalId: string; resolveApproval: (approval: Approval, allow: boolean) => Promise<void> }) {
  const hasReasoningEvidence = run.reasoningObserved || run.reasoningTokens > 0;
  if (!toolCalls.length && !approvals.length && !hasReasoningEvidence) return null;
  // The database keeps the complete tool timeline for replay and audit, but
  // the chat surface only needs the newest activity. Pending approvals are
  // additionally retained so an approval action can never disappear.
  const newestCall = toolCalls.reduce<ToolCall | undefined>((latest, call) => {
    if (!latest) return call;
    return call.createdAt >= latest.createdAt ? call : latest;
  }, undefined);
  const approvalCallIds = new Set(approvals.map((approval) => approval.toolCallId));
  const shownCalls = toolCalls.filter((call) => call.id === newestCall?.id || approvalCallIds.has(call.id));
  return <section className="run-activity" aria-label="工具调用时间线">
    {hasReasoningEvidence && <details className="reasoning-card">
      <summary><span className="reasoning-card-icon"><Icon name="spark" size={14}/></span><span><b>已观察到思考</b><small>{run.reasoningTokens > 0 ? `${run.reasoningTokens.toLocaleString()} 推理 Token` : "供应商返回了推理状态"}</small></span><i>查看证据</i></summary>
      <div className="reasoning-evidence"><p>SciAide 只展示可核验的状态，不把原始 thinking、signature 或 encrypted content 暴露到聊天界面。</p><span>推理状态已观察</span>{run.reasoningSignatureObserved && <span>Anthropic 签名已保存</span>}{run.reasoningTokens > 0 && <span>推理 Token：{run.reasoningTokens.toLocaleString()}</span>}</div>
    </details>}
    {shownCalls.map((call) => {
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

const knowledgeStatusText: Record<KnowledgeDocument["status"], string> = { pending: "等待索引", indexing: "正在索引", ready: "可检索", failed: "索引失败" };
const documentKind = (name: string) => name.includes(".") ? name.split(".").pop()?.toUpperCase() ?? "FILE" : "FILE";
const compactDate = (value: string) => new Date(value).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });

function KnowledgeLibrary({ project, close }: { project: Project; close: () => void }) {
  const [documents, setDocuments] = useState<KnowledgeDocument[]>([]);
  const [loading, setLoading] = useState(true);
  const [importing, setImporting] = useState(false);
  const [removing, setRemoving] = useState("");
  const [feedback, setFeedback] = useState("");
  const [embedding, setEmbedding] = useState<EmbeddingConfig | null>(null);
  const [embeddingOpen, setEmbeddingOpen] = useState(false);
  const [embeddingEnabled, setEmbeddingEnabled] = useState(false);
  const [embeddingBaseUrl, setEmbeddingBaseUrl] = useState("");
  const [embeddingModelId, setEmbeddingModelId] = useState("");
  const [embeddingKey, setEmbeddingKey] = useState("");
  const [savingEmbedding, setSavingEmbedding] = useState(false);

  const load = useCallback(async (quiet = false) => {
    if (!quiet) setLoading(true);
    try { setDocuments(await backend<KnowledgeDocument[]>("KnowledgeFacade", "ListDocuments", project.id)); }
    catch (error) { if (!quiet) setFeedback(errorText(error)); }
    finally { if (!quiet) setLoading(false); }
  }, [project.id]);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    void backend<EmbeddingConfig>("KnowledgeFacade", "GetEmbeddingConfig").then((value) => {
      setEmbedding(value); setEmbeddingEnabled(value.enabled); setEmbeddingBaseUrl(value.baseUrl); setEmbeddingModelId(value.modelId); setEmbeddingKey("");
    }).catch((error: unknown) => setFeedback(errorText(error)));
  }, []);
  useEffect(() => {
    if (!documents.some((item) => item.status === "pending" || item.status === "indexing")) return;
    const timer = window.setInterval(() => void load(true), 900);
    return () => window.clearInterval(timer);
  }, [documents, load]);

  async function addDocuments() {
    if (importing) return;
    setImporting(true); setFeedback("");
    try {
      const result = await backend<AttachmentImportBatch>("KnowledgeFacade", "ChooseAndImportDocuments", project.id);
      await load(true);
      if (result.errors.length) setFeedback(`有 ${result.errors.length} 个文件未能加入知识库：${first(result.errors)?.message ?? "未知错误"}`);
      else if (result.attachments.length) setFeedback(`已提交 ${result.attachments.length} 个文件，正在建立项目索引。`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setImporting(false); }
  }

  async function removeDocument(value: KnowledgeDocument) {
    if (removing || !window.confirm(`将“${value.title}”移出项目知识库？\n\n索引会删除，但原附件和历史聊天记录仍会保留。`)) return;
    setRemoving(value.id); setFeedback("");
    try {
      await backend("KnowledgeFacade", "RemoveDocument", project.id, value.id);
      setDocuments((current) => current.filter((item) => item.id !== value.id));
      setFeedback("已移出知识库，原附件仍保留在项目中。");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setRemoving(""); }
  }

  async function saveEmbedding() {
    if (savingEmbedding) return;
    setSavingEmbedding(true); setFeedback(embeddingEnabled ? "正在验证 /v1/embeddings…" : "");
    try {
      const value = await backend<EmbeddingConfig>("KnowledgeFacade", "SaveEmbeddingConfig", project.id, {
        enabled: embeddingEnabled, baseUrl: embeddingBaseUrl, modelId: embeddingModelId, apiKey: embeddingKey, timeoutSeconds: embedding?.timeoutSeconds || 30,
      });
      setEmbedding(value); setEmbeddingEnabled(value.enabled); setEmbeddingBaseUrl(value.baseUrl); setEmbeddingModelId(value.modelId); setEmbeddingKey("");
      await load(true);
      setFeedback(value.enabled ? `语义检索已验证：${value.modelId} · ${value.dimensions} 维，正在影子重建项目索引。` : "已关闭语义检索，继续使用 FTS5/BM25。");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setSavingEmbedding(false); }
  }

  const ready = documents.filter((item) => item.status === "ready").length;
  const working = documents.filter((item) => item.status === "pending" || item.status === "indexing").length;
  const chunks = documents.reduce((total, item) => total + item.chunkCount, 0);
  return <div className="modal-backdrop"><section className="model-modal knowledge-modal">
    <header><div><span className="dialog-icon gradient"><Icon name="library" size={19}/></span><div><p>PROJECT KNOWLEDGE</p><h2>{project.name} · 知识库</h2></div></div><div className="knowledge-header-actions"><button type="button" className="knowledge-add" disabled={importing} onClick={() => void addDocuments()}><Icon name={importing ? "refresh" : "plus"} size={15}/>{importing ? "正在导入" : "新增文件"}</button><button type="button" className="close" onClick={close}><Icon name="close"/></button></div></header>
    <div className="knowledge-body">
      <div className="knowledge-summary"><div><span>文档总数</span><b>{documents.length}</b></div><div><span>可检索</span><b>{ready}</b></div><div><span>索引片段</span><b>{chunks.toLocaleString()}</b></div>{working > 0 && <div className="knowledge-working"><Icon name="refresh" size={13}/><span>{working} 个任务处理中</span></div>}</div>
      <div className="knowledge-boundary"><Icon name="shield" size={16}/><div><b>仅显式导入的文件会进入项目知识库</b><span>知识库内容可跨会话检索；聊天框右下角添加的附件只供当前对话读取，不会出现在这里。</span></div></div>
      <section className={`knowledge-retrieval ${embeddingOpen ? "open" : ""}`}>
        <button type="button" className="knowledge-retrieval-toggle" onClick={() => setEmbeddingOpen((value) => !value)}><span><Icon name="search" size={15}/></span><div><b>检索方式</b><small>{embedding?.enabled ? `混合检索 · ${embedding.modelId} · ${embedding.dimensions} 维` : "FTS5/BM25 · 不使用 Embedding"}</small></div><Icon name="settings" size={14}/></button>
        {embeddingOpen && <div className="embedding-settings">
          <label className="embedding-switch"><input type="checkbox" checked={embeddingEnabled} onChange={(event) => setEmbeddingEnabled(event.target.checked)}/><span/><div><b>启用语义检索</b><small>关闭时不会请求 Embedding API</small></div></label>
          <div className="embedding-fields"><label>Base URL<input value={embeddingBaseUrl} disabled={!embeddingEnabled} onChange={(event) => setEmbeddingBaseUrl(event.target.value)} placeholder="http://127.0.0.1:8000/v1"/></label><label>Embedding Model ID<input value={embeddingModelId} disabled={!embeddingEnabled} onChange={(event) => setEmbeddingModelId(event.target.value)} placeholder="例如：Qwen3-Embedding-0.6B"/></label><label>API Key <small>{embedding?.secretConfigured ? `已保存 ${embedding.secretMasked}` : "本地服务可留空"}</small><input className="api-key-input" type="password" autoComplete="new-password" disabled={!embeddingEnabled} value={embeddingKey} onChange={(event) => setEmbeddingKey(event.target.value)} placeholder={embedding?.secretConfigured ? "留空保持现有密钥" : "可选"}/></label><button type="button" disabled={savingEmbedding || (embeddingEnabled && (!embeddingBaseUrl.trim() || !embeddingModelId.trim()))} onClick={() => void saveEmbedding()}>{savingEmbedding && <Icon name="refresh" size={13}/>} {embeddingEnabled ? "保存并验证" : "保存"}</button></div>
          <p>此配置供所有项目复用，各项目分别保存向量索引。接口固定请求 <code>{embeddingBaseUrl.replace(/\/$/, "") || "{Base URL}"}/embeddings</code>；失败时搜索自动回退到 FTS5/BM25。</p>
        </div>}
      </section>
      {feedback && <div className="knowledge-feedback"><span>{feedback}</span><button type="button" onClick={() => setFeedback("")}><Icon name="close" size={13}/></button></div>}
      <div className="knowledge-list-head"><span>文档</span><span>状态</span><span>片段</span><span>操作</span></div>
      <div className="knowledge-list">{loading ? <div className="knowledge-empty"><Icon name="refresh" size={24}/><b>正在读取项目知识库</b></div> : documents.length === 0 ? <div className="knowledge-empty"><span><Icon name="library" size={27}/></span><b>知识库还是空的</b><p>添加论文、数据表或研究笔记，之后可以在当前项目的任意会话中统一检索。</p><button type="button" onClick={() => void addDocuments()}><Icon name="plus" size={14}/> 新增文件</button></div> : documents.map((item) => <div className="knowledge-row" key={item.id}>
        <span className="knowledge-file-icon"><Icon name="skill" size={17}/></span><div className="knowledge-file"><b title={item.title}>{item.title}</b><small>{documentKind(item.title)} · {item.chunkingVersion} · {compactDate(item.createdAt)}</small>{item.errorMessage && <em title={item.errorMessage}>{item.errorMessage}</em>}</div>
        <span className={`knowledge-status ${item.status}`}>{item.status === "indexing" && <Icon name="refresh" size={11}/>} {knowledgeStatusText[item.status]}</span>
        <div className="knowledge-chunks"><b>{item.chunkCount.toLocaleString()}</b><small>chunks</small></div>
        <button type="button" className={`knowledge-remove ${removing === item.id ? "loading" : ""}`} aria-label={`将 ${item.title} 移出知识库`} title="移出知识库但保留原附件" disabled={Boolean(removing)} onClick={() => void removeDocument(item)}><Icon name={removing === item.id ? "refresh" : "trash"} size={15}/></button>
      </div>)}</div>
    </div>
  </section></div>;
}

const compareSkillVersions = (left: string, right: string) => {
  const parse = (value: string) => {
    const withoutBuild = value.split("+", 1)[0] ?? value;
    const parts = withoutBuild.split("-", 2);
    const core = parts[0] ?? "";
    const prerelease = parts[1] ?? "";
    return { numbers: core.split(".").map((part) => Number.parseInt(part, 10) || 0), prerelease: prerelease ? prerelease.split(".") : [] };
  };
  const a = parse(left); const b = parse(right);
  for (let index = 0; index < Math.max(a.numbers.length, b.numbers.length); index += 1) {
    const difference = (a.numbers[index] ?? 0) - (b.numbers[index] ?? 0);
    if (difference) return difference;
  }
  if (!a.prerelease.length && b.prerelease.length) return 1;
  if (a.prerelease.length && !b.prerelease.length) return -1;
  for (let index = 0; index < Math.max(a.prerelease.length, b.prerelease.length); index += 1) {
    const leftPart = a.prerelease[index]; const rightPart = b.prerelease[index];
    if (leftPart === undefined) return -1;
    if (rightPart === undefined) return 1;
    if (leftPart === rightPart) continue;
    const leftNumeric = /^\d+$/.test(leftPart); const rightNumeric = /^\d+$/.test(rightPart);
    if (leftNumeric && rightNumeric) return Number(leftPart) - Number(rightPart);
    if (leftNumeric !== rightNumeric) return leftNumeric ? -1 : 1;
    return leftPart < rightPart ? -1 : 1;
  }
  return 0;
};
const shortHash = (value?: string) => value ? value.slice(0, 12) : "未记录";
const sourceKindText = (kind?: SkillSource["kind"]) => kind === "builtin" ? "SciAide 内置" : kind === "zip" ? "ZIP" : kind === "folder" ? "文件夹" : "目录扫描";

function SkillSettings({ project, close }: { project?: Project; close: () => void }) {
  const [skills, setSkills] = useState<InstalledSkill[]>([]);
  const [projectSkills, setProjectSkills] = useState<ProjectSkillView[]>([]);
  const [selectedSkillId, setSelectedSkillId] = useState("");
  const [version, setVersion] = useState("");
  const [enabled, setEnabled] = useState(false);
  const [priority, setPriority] = useState(100);
  const [rollbackVersion, setRollbackVersion] = useState("");
  const [installOpen, setInstallOpen] = useState(false);
  const [sourceKind, setSourceKind] = useState<"folder" | "zip">("folder");
  const [sourcePath, setSourcePath] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [toast, setToast] = useState<{ id: number; text: string; detail: string } | null>(null);

  const groups = useMemo(() => {
    const values = new Map<string, InstalledSkill[]>();
    skills.forEach((item) => values.set(item.manifest.id, [...(values.get(item.manifest.id) ?? []), item]));
    return [...values.entries()].map(([id, versions]) => ({ id, versions: versions.sort((a, b) => compareSkillVersions(b.manifest.version, a.manifest.version)) }))
      .sort((a, b) => (a.versions[0]?.manifest.name ?? a.id).localeCompare(b.versions[0]?.manifest.name ?? b.id));
  }, [skills]);
  const selectedGroup = groups.find((group) => group.id === selectedSkillId);
  const selected = selectedGroup?.versions.find((item) => item.manifest.version === version) ?? selectedGroup?.versions[0];
  const projectLink = projectSkills.find((item) => item.skillId === selectedSkillId);
  const rollbackOptions = selectedGroup && projectLink
    ? selectedGroup.versions.filter((item) => compareSkillVersions(item.manifest.version, projectLink.version) < 0 && item.availability === "available")
    : [];

  const load = useCallback(async () => {
    const [installed, links] = await Promise.all([
      backend<InstalledSkill[]>("SkillFacade", "ListInstalledSkills"),
      project ? backend<ProjectSkillView[]>("SkillFacade", "ListProjectSkills", project.id) : Promise.resolve([]),
    ]);
    const installedValues = installed ?? [];
    setSkills(installedValues);
    setProjectSkills(links ?? []);
    setSelectedSkillId((current) => installedValues.some((item) => item.manifest.id === current) ? current : installedValues[0]?.manifest.id ?? "");
  }, [project]);

  useEffect(() => {
    setLoading(true);
    load().catch((error: unknown) => setFeedback(errorText(error))).finally(() => setLoading(false));
  }, [load]);
  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(null), 2800);
    return () => window.clearTimeout(timer);
  }, [toast]);
  useEffect(() => {
    if (!selectedGroup) { setVersion(""); setEnabled(false); setPriority(100); return; }
    const link = projectSkills.find((item) => item.skillId === selectedGroup.id);
    setVersion(link?.version ?? selectedGroup.versions[0]?.manifest.version ?? "");
    setEnabled(link?.enabled ?? false);
    setPriority(link?.priority ?? 100);
  }, [projectSkills, selectedGroup?.id]);
  useEffect(() => { setRollbackVersion(rollbackOptions[0]?.manifest.version ?? ""); }, [projectLink?.version, selectedSkillId, skills]);

  function showToast(text: string, detail: string) { setToast({ id: Date.now(), text, detail }); }

  async function refreshCatalog() {
    setBusy(true); setFeedback("");
    try {
      const result = await backend<SkillRefreshResult>("SkillFacade", "RefreshSkills");
      await load();
      showToast("Skill 目录已刷新", `有效 ${result.valid} · 异常 ${result.invalid} · 缺失 ${result.missing}`);
      if (result.diagnostics?.length) setFeedback(result.diagnostics.map((item) => item.message).join("；"));
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusy(false); }
  }

  async function chooseSource(kind = sourceKind) {
    try {
      const path = await backend<string>("SkillFacade", kind === "zip" ? "ChooseSkillZIP" : "ChooseSkillFolder");
      if (path) { setSourceKind(kind); setSourcePath(path); }
    } catch (error) { setFeedback(errorText(error)); }
  }

  async function install(event: FormEvent) {
    event.preventDefault();
    if (!sourcePath.trim()) return;
    setBusy(true); setFeedback("");
    try {
      let result: SkillInstallResult;
      try {
        result = await backend<SkillInstallResult>("SkillFacade", "InstallSkill", { sourcePath, sourceKind, replaceExisting: false });
      } catch (error) {
        const message = errorText(error);
        const replacementRequired = message.includes("explicit replacement is required") || message.includes("already recorded with different content");
        if (!replacementRequired || !window.confirm("检测到相同 Skill ID 和版本，但包内容不同。\n\n替换会更新该版本的安装内容和来源记录；正在运行的会话仍使用其不可变快照。确认替换？")) throw error;
        result = await backend<SkillInstallResult>("SkillFacade", "InstallSkill", { sourcePath, sourceKind, replaceExisting: true });
      }
      await load();
      setSelectedSkillId(result.skill.manifest.id); setInstallOpen(false); setSourcePath("");
      showToast(result.replaced ? "Skill 已替换" : result.idempotent ? "Skill 已存在" : "Skill 已安装", `${result.skill.manifest.name} · v${result.skill.manifest.version}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusy(false); }
  }

  async function saveProjectSkill() {
    if (!project || !selected) return;
    setBusy(true); setFeedback("");
    try {
      const saved = await backend<ProjectSkillView>("SkillFacade", "SetProjectSkill", { projectId: project.id, skillId: selected.manifest.id, version: selected.manifest.version, enabled, priority });
      await load();
      showToast("项目 Skill 已保存", `${saved.skill.manifest.name} · v${saved.version} · ${saved.enabled ? "已启用" : "已禁用"}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusy(false); }
  }

  async function uninstallSkill() {
    if (!selected || !window.confirm(`卸载 ${selected.manifest.name} v${selected.manifest.version}？\n\n安装包会移入可恢复备份；默认不会删除任何项目引用。`)) return;
    setBusy(true); setFeedback("");
    try {
      let result: SkillUninstallResult;
      try {
        result = await backend<SkillUninstallResult>("SkillFacade", "UninstallSkill", { skillId: selected.manifest.id, version: selected.manifest.version, removeProjectLinks: false });
      } catch (error) {
        const message = errorText(error);
        if (!message.includes("referenced by") || !window.confirm("该版本仍被一个或多个项目引用。\n\n继续会同时移除这些项目的 Skill 配置，但不会改变已经开始运行的 Run 快照。确认继续？")) throw error;
        result = await backend<SkillUninstallResult>("SkillFacade", "UninstallSkill", { skillId: selected.manifest.id, version: selected.manifest.version, removeProjectLinks: true });
      }
      await load();
      showToast("Skill 已卸载", `${result.skillId} · v${result.version}${result.removedProjectLinks ? ` · 移除 ${result.removedProjectLinks} 个项目引用` : ""}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusy(false); }
  }

  async function rollback() {
    if (!project || !projectLink || !rollbackVersion || !window.confirm(`将 ${projectLink.skillId} 从 v${projectLink.version} 回滚到 v${rollbackVersion}？`)) return;
    setBusy(true); setFeedback("");
    try {
      const result = await backend<SkillRollbackResult>("SkillFacade", "RollbackProjectSkill", { projectId: project.id, skillId: projectLink.skillId, targetVersion: rollbackVersion });
      await load();
      showToast("项目 Skill 已回滚", `v${result.fromVersion} → v${result.toVersion}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusy(false); }
  }

  return <div className="modal-backdrop">
    <section className="model-modal skill-modal" role="dialog" aria-modal="true">
      {toast && <div className="mcp-toast" role="status"><span><Icon name="check" size={15}/></span><div><b>{toast.text}</b><small>{toast.detail}</small></div></div>}
      <header><div><span className="dialog-icon gradient"><Icon name="skill"/></span><div><p>RESEARCH SKILL PACKAGES</p><h2>Skills</h2></div></div><button className="close" onClick={close} aria-label="关闭"><Icon name="close"/></button></header>
      <div className="settings-grid">
        <aside>
          <button className={`add-profile ${installOpen ? "selected" : ""}`} onClick={() => { setInstallOpen(true); setFeedback(""); }} disabled={busy}><Icon name="plus"/> 安装本地 Skill</button>
          <button className="skill-refresh" onClick={() => void refreshCatalog()} disabled={busy}><Icon name="refresh" size={14}/> 刷新目录</button>
          <div className="profile-caption">已安装 · {groups.length}</div>
          {loading ? <p className="skill-side-empty">正在读取 Skill 目录…</p> : groups.length ? groups.map((group) => {
            const link = projectSkills.find((item) => item.skillId === group.id);
            const representative = group.versions.find((item) => item.manifest.version === link?.version) ?? group.versions[0];
            if (!representative) return null;
            const healthy = group.versions.some((item) => item.availability === "available");
            return <button key={group.id} className={`profile-item skill-profile ${!installOpen && group.id === selectedSkillId ? "selected" : ""}`} onClick={() => { setInstallOpen(false); setSelectedSkillId(group.id); setFeedback(""); }} disabled={busy}><span className="provider-logo"><Icon name="skill" size={15}/></span><span><b>{representative.manifest.name}</b><small>{group.versions.length} 个版本{link ? ` · ${link.enabled ? "当前项目已启用" : "当前项目已禁用"}` : ""}</small></span><i className={`status-dot ${healthy ? "ready" : "failed"}`}/></button>;
          }) : <p className="skill-side-empty">还没有安装 Skill。</p>}
        </aside>
        {installOpen ? <form className="skill-install" onSubmit={(event) => void install(event)}>
          <section className="form-section"><div className="form-heading"><span>01</span><div><h3>安装本地 Skill</h3><p>支持 SciAide 文件夹、Codex 风格 SKILL.md 文件夹或 ZIP 包</p></div></div>
            <div className="skill-source-tabs"><button type="button" className={sourceKind === "folder" ? "active" : ""} onClick={() => { setSourceKind("folder"); setSourcePath(""); }} disabled={busy}><Icon name="folder" size={15}/> 文件夹</button><button type="button" className={sourceKind === "zip" ? "active" : ""} onClick={() => { setSourceKind("zip"); setSourcePath(""); }} disabled={busy}><Icon name="skill" size={15}/> ZIP 包</button></div>
            <label>本地来源<div className="skill-path-picker"><input value={sourcePath} onChange={(event) => setSourcePath(event.target.value)} placeholder={sourceKind === "zip" ? "选择 .zip 文件" : "选择包含 skill.yaml 或 SKILL.md 的文件夹"} required disabled={busy}/><button type="button" onClick={() => void chooseSource()} disabled={busy}>浏览…</button></div><small>包会先进入随机暂存目录，完成格式、路径、大小和哈希校验后再原子安装。</small></label>
            <div className="skill-security-note"><Icon name="shield" size={17}/><div><b>安装不会执行包内代码</b><span>Skill 内容属于不可信数据。脚本不会自动运行，Manifest 权限也不能绕过 Plan/Full Access、Workspace 边界或工具审批。</span></div></div>
          </section>
          {feedback && <div className="feedback error">{feedback}</div>}
          <footer className="modal-actions"><span/><span/><button type="button" onClick={() => { setSourcePath(""); setFeedback(""); }} disabled={busy || !sourcePath}>清空</button><button className="primary" disabled={busy || !sourcePath.trim()}>{busy ? "校验并安装中…" : "校验并安装"}</button></footer>
        </form> : <div className="skill-detail">
          {loading ? <div className="skill-page-state">正在加载已安装 Skill…</div> : selected ? <>
            <section className="skill-overview"><div className="skill-title"><span className="skill-large-icon"><Icon name="skill" size={22}/></span><div><div><h3>{selected.manifest.name}</h3><code>${selected.manifest.id}</code></div><p>{selected.manifest.description || "未提供描述"}</p></div></div><div className="skill-state-row"><span className={`skill-badge ${selected.integrity}`}>完整性 · {selected.integrity}</span><span className={`skill-badge ${selected.availability}`}>{selected.availability === "available" ? "可用于项目" : "当前不可用"}</span><span className="skill-badge neutral">{selected.manifest.activation.mode === "suggest" ? "自动建议" : "显式调用"}</span></div></section>
            <section className="skill-section"><div className="skill-section-title"><div><h4>版本与来源</h4><p>项目固定到具体版本，升级不会静默改变已有配置</p></div></div><div className="skill-version-grid"><label>查看版本<select value={selected.manifest.version} onChange={(event) => setVersion(event.target.value)} disabled={busy}>{selectedGroup?.versions.map((item) => <option value={item.manifest.version} key={item.manifest.version}>v{item.manifest.version}{item.availability !== "available" ? " · 不可用" : ""}</option>)}</select></label><div><span>安装来源</span><b>{sourceKindText(selected.source.kind)} · {selected.source.name || "本地目录"}</b><small>{selected.source.archived ? "来源已归档" : "来源未归档"} · SHA256 {shortHash(selected.source.hash)}</small></div><div><span>包校验</span><b>Package {shortHash(selected.packageHash)}</b><small>Manifest {shortHash(selected.manifestHash)} · Content {shortHash(selected.contentHash)}</small></div></div></section>
            {(selected.availabilityReason || selected.integrityError || selected.missingRequiredTools.length > 0 || selected.missingOptionalTools.length > 0) && <section className="skill-diagnostics"><b>依赖与诊断</b>{selected.integrityError && <p className="error">{selected.integrityError}</p>}{selected.availabilityReason && <p className="error">{selected.availabilityReason}</p>}{selected.missingRequiredTools.length > 0 && <p className="error">缺少必需 Tool：{selected.missingRequiredTools.join("、")}</p>}{selected.missingOptionalTools.length > 0 && <p className="warning">缺少可选 Tool：{selected.missingOptionalTools.join("、")}</p>}</section>}
            <section className="skill-section"><div className="skill-section-title"><div><h4>激活规则</h4><p>启用只让 Skill 进入项目 catalog，不会把正文永久塞入每轮对话</p></div></div><div className="skill-facts"><div><span>激活模式</span><b>{selected.manifest.activation.mode === "suggest" ? "Suggest · 确定性触发" : `Explicit · 使用 $${selected.manifest.id}`}</b></div><div><span>上下文上限</span><b>{selected.manifest.context.maxTokens.toLocaleString()} tokens</b></div><div><span>SciAide 兼容</span><b>{selected.manifest.compatibility.sciaide || "未声明"}</b></div></div>{selected.manifest.activation.triggers.length > 0 && <div className="skill-chips"><b>Triggers</b>{selected.manifest.activation.triggers.map((item) => <span key={item}>{item}</span>)}</div>}{selected.manifest.permissions.length > 0 && <div className="skill-chips muted"><b>权限声明（仅审计）</b>{selected.manifest.permissions.map((item) => <span key={item}>{item}</span>)}</div>}</section>
            <section className="skill-section project-skill-section"><div className="skill-section-title"><div><h4>当前项目</h4><p>{project ? project.name : "请先在主界面选择一个 Workspace"}</p></div>{projectLink && <span className={projectLink.enabled ? "project-link-on" : "project-link-off"}>{projectLink.enabled ? "已启用" : "已禁用"} · v{projectLink.version}</span>}</div>
              {project ? <div className="project-skill-controls"><label>项目版本<select value={version} onChange={(event) => setVersion(event.target.value)} disabled={busy}>{selectedGroup?.versions.map((item) => <option value={item.manifest.version} key={item.manifest.version}>v{item.manifest.version}{item.availability !== "available" ? " · 不可用" : ""}</option>)}</select></label><label>优先级 <small>0–1000，数值越小越靠前</small><input type="number" min={0} max={1000} value={priority} onChange={(event) => setPriority(Math.max(0, Math.min(1000, Number(event.target.value))))} disabled={busy}/></label><label className="skill-enable"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} disabled={busy}/><span><b>在当前项目启用</b><small>只有可用版本可以启用；禁用后新 Run 不再选择该 Skill</small></span></label><button type="button" className="skill-save" onClick={() => void saveProjectSkill()} disabled={busy || (enabled && selected.availability !== "available")}>{busy ? "保存中…" : "保存项目配置"}</button></div> : <div className="skill-no-project"><Icon name="folder" size={17}/> 仍可查看、安装和卸载 Skill；选择项目后才能设置版本与启用状态。</div>}
              {projectLink && rollbackOptions.length > 0 && <div className="skill-rollback"><div><b>回滚项目版本</b><small>仅显示已安装且可用的更低版本</small></div><select value={rollbackVersion} onChange={(event) => setRollbackVersion(event.target.value)} disabled={busy}>{rollbackOptions.map((item) => <option key={item.manifest.version} value={item.manifest.version}>v{item.manifest.version}</option>)}</select><button type="button" onClick={() => void rollback()} disabled={busy || !rollbackVersion}>回滚</button></div>}
            </section>
            <footer className="skill-detail-actions">{selected.source.kind === "builtin" ? <div className="skill-builtin-note"><Icon name="shield" size={14}/> 内置原版随 SciAide 提供，可按项目禁用或显式替换。</div> : <button type="button" className="danger" onClick={() => void uninstallSkill()} disabled={busy}><Icon name="trash" size={14}/> 卸载此版本</button>}<span>安装于 {new Date(selected.installedAt).toLocaleDateString()}</span></footer>
          </> : <div className="skill-page-state"><span className="skill-large-icon"><Icon name="skill" size={22}/></span><b>尚未安装 Skill</b><p>从左侧选择“安装本地 Skill”，添加经过校验的研究工作流。</p></div>}
          {feedback && <div className="feedback error">{feedback}</div>}
        </div>}
      </div>
    </section>
  </div>;
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
        <section className="usage-token-grid"><article><span>实际输入</span><b>{usageNumber(summary.freshInputTokens)}</b><small>排除缓存后的新输入</small></article><article><span>模型输出</span><b>{usageNumber(summary.outputTokens)}</b><small>模型生成 Token</small></article><article className="reasoning-token"><span>其中推理</span><b>{usageNumber(summary.reasoningTokens)}</b><small>供应商明确报告的推理 Token</small></article><article className="cache-read"><span>缓存读取</span><b>{usageNumber(summary.cacheReadTokens)}</b><small>本次命中的输入缓存</small></article><article className="cache-create"><span>缓存创建</span><b>{usageNumber(summary.cacheCreationTokens)}</b><small>写入供后续复用的缓存</small></article></section>
        <div className="usage-panels">
          <section className="usage-panel"><div className="usage-panel-title"><div><h3>日期趋势</h3><p>按系统本地日期汇总真实 Token</p></div></div>{data.daily.length ? <div className="usage-trend">{data.daily.map((item) => <div className="trend-row" key={item.date}><time>{item.date}</time><div className="trend-track"><i style={{ width: `${Math.max(2, item.realTotalTokens / maxDaily * 100)}%` }}/></div><b>{usageNumber(item.realTotalTokens)}</b><span>{usageRate(item)}</span></div>)}</div> : <div className="usage-empty">所选日期范围内还没有用量记录</div>}</section>
          <section className="usage-panel model-usage-panel"><div className="usage-panel-title"><div><h3>按模型统计</h3><p>百分比由各模型 Token 汇总后独立计算</p></div></div>{data.models.length ? <div className="usage-table"><div className="usage-table-head"><span>模型 / API</span><span>实际输入</span><span>缓存读取</span><span>输出</span><span>推理</span><span>命中率</span></div>{data.models.map((item) => <div className="usage-table-row" key={`${item.modelProfileId}\t${item.modelId}`}><span><b>{item.modelId}</b><small>{item.profileName || "未知配置"}</small></span><span>{usageNumber(item.freshInputTokens)}</span><span>{usageNumber(item.cacheReadTokens)}</span><span>{usageNumber(item.outputTokens)}</span><span>{usageNumber(item.reasoningTokens)}</span><span className={item.cacheDataAvailable ? "rate" : "muted"}>{usageRate(item)}</span></div>)}</div> : <div className="usage-empty">没有匹配的模型记录</div>}</section>
        </div>
        <p className="usage-method"><Icon name="shield" size={13}/> OpenAI-compatible 的 <code>prompt_tokens</code> 会先扣除缓存读取与创建，得到“实际输入”。命中率 = 缓存读取 ÷（实际输入 + 缓存创建 + 缓存读取）；未返回缓存字段的轮次不会被误算为未命中。</p>
      </>}
    </div>
  </section></div>;
}

function ModelSettings({
  profiles,
  close,
  refresh,
  select,
}: {
  profiles: Profile[];
  close: () => void;
  refresh: () => Promise<void>;
  select: (id: string) => void;
}) {
  const [id, setId] = useState("");
  const current = profiles.find((item) => item.id === id);
  const [name, setName] = useState("");
  const [apiProtocol, setAPIProtocol] = useState<APIProtocol>(
    "openai_chat_completions",
  );
  const [baseUrl, setBaseUrl] = useState("https://api.openai.com/v1");
  const [profileModels, setProfileModels] = useState<ProfileModel[]>([]);
  const [manualModelId, setManualModelId] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [headers, setHeaders] = useState("");
  const [models, setModels] = useState<AvailableModel[]>([]);
  const [modelSearch, setModelSearch] = useState("");
  const [discovering, setDiscovering] = useState(false);
  const [feedback, setFeedback] = useState<{
    kind: "ok" | "error" | "info";
    text: string;
  } | null>(null);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    setName(current?.name ?? "");
    setAPIProtocol(current?.apiProtocol ?? "openai_chat_completions");
    setBaseUrl(current?.baseUrl ?? "https://api.openai.com/v1");
    setProfileModels(current?.models ?? []);
    setManualModelId("");
    setApiKey("");
    setHeaders(
      current && Object.keys(current.customHeaders).length
        ? JSON.stringify(current.customHeaders)
        : "",
    );
    setModels([]);
    setModelSearch("");
    setFeedback(null);
  }, [current]);
  const filteredModels = useMemo(
    () =>
      models.filter((item) =>
        item.id.toLowerCase().includes(modelSearch.trim().toLowerCase()),
      ),
    [modelSearch, models],
  );
  function parsedHeaders() {
    return headers.trim()
      ? (JSON.parse(headers) as Record<string, string>)
      : {};
  }
  function toggleModel(item: AvailableModel) {
    setProfileModels((currentModels) => {
      const exists = currentModels.some((model) => model.id === item.id);
      if (exists) {
        const remaining = currentModels.filter((model) => model.id !== item.id);
        return remaining.map((model, index) => ({
          ...model,
          isDefault:
            index === 0
              ? !remaining.some((candidate) => candidate.isDefault)
              : model.isDefault,
        }));
      }
      const declared =
        item.reasoningCapabilitySource === "provider"
          ? (item.reasoningLevels ?? [])
          : [];
      const levels = declared.length
        ? declared
        : inferredReasoningLevels(apiProtocol, item.id);
      return [
        ...currentModels,
        {
          id: item.id,
          ownedBy: item.ownedBy,
          enabled: true,
          isDefault: currentModels.length === 0,
          contextWindowTokens:
            item.contextWindowTokens || defaultContextWindowTokens,
          autoCompactTokenLimit:
            item.autoCompactTokenLimit ||
            automaticCompactLimit(
              item.contextWindowTokens || defaultContextWindowTokens,
            ),
          contextWindowSource:
            item.contextWindowSource === "provider" ? "provider" : "fallback",
          reasoningLevels: levels,
          reasoningCapabilitySource: declared.length
            ? "provider"
            : levels.length
              ? "inferred"
              : "unsupported",
        },
      ];
    });
  }
  function addManualModel() {
    const value = manualModelId.trim();
    if (!value || profileModels.some((model) => model.id === value)) return;
    const inferred = inferredReasoningLevels(apiProtocol, value);
    setProfileModels((currentModels) => [
      ...currentModels,
      {
        id: value,
        enabled: true,
        isDefault: currentModels.length === 0,
        contextWindowTokens: defaultContextWindowTokens,
        autoCompactTokenLimit: automaticCompactLimit(
          defaultContextWindowTokens,
        ),
        contextWindowSource: "fallback",
        reasoningLevels: inferred,
        reasoningCapabilitySource: inferred.length ? "inferred" : "unsupported",
      },
    ]);
    setManualModelId("");
  }
  function setDefaultModel(modelId: string) {
    setProfileModels((currentModels) =>
      currentModels.map((model) => ({
        ...model,
        enabled: true,
        isDefault: model.id === modelId,
      })),
    );
  }
  function setModelContextWindow(modelId: string, tokens: number) {
    const normalized = Math.max(
      4_096,
      Math.min(10_000_000, Math.trunc(tokens || defaultContextWindowTokens)),
    );
    setProfileModels((currentModels) =>
      currentModels.map((model) =>
        model.id === modelId
          ? {
              ...model,
              contextWindowTokens: normalized,
              autoCompactTokenLimit: automaticCompactLimit(normalized),
              contextWindowSource: "manual",
            }
          : model,
      ),
    );
  }
  async function discover() {
    setDiscovering(true);
    setFeedback({ kind: "info", text: "正在读取 /v1/models…" });
    try {
      const values = await backend<AvailableModel[]>(
        "ModelFacade",
        "DiscoverModels",
        {
          profileId: id,
          apiProtocol,
          baseUrl,
          apiKey,
          customHeaders: parsedHeaders(),
        },
      );
      setModels(values);
      setFeedback(
        values.length
          ? {
              kind: "ok",
              text: `已获取 ${values.length} 个模型，可勾选多个模型共用该 API Key。`,
            }
          : { kind: "info", text: "服务返回了空列表，请手动添加 Model ID。" },
      );
    } catch (error) {
      setModels([]);
      setFeedback({
        kind: "error",
        text:
          apiProtocol === "anthropic_messages"
            ? "Anthropic 原生服务通常不提供 /v1/models，请直接手动添加 Model ID。"
            : errorText(error),
      });
    } finally {
      setDiscovering(false);
    }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    const defaultModel =
      profileModels.find((model) => model.isDefault) ?? profileModels[0];
    if (!defaultModel) {
      setFeedback({ kind: "error", text: "请至少选择或手动添加一个模型。" });
      return;
    }
    setSaving(true);
    setFeedback(null);
    try {
      const saved = await backend<Profile>("ModelFacade", "SaveModelProfile", {
        id,
        name,
        apiProtocol,
        baseUrl,
        modelId: defaultModel.id,
        models: profileModels,
        apiKey,
        timeoutSeconds: current?.timeoutSeconds ?? 60,
        customHeaders: parsedHeaders(),
        enabled: true,
        isDefault: profiles.length === 0 || current?.isDefault === true,
      });
      await refresh();
      setId(saved.id);
      select(saved.id);
      setApiKey("");
      setFeedback({
        kind: "ok",
        text: `配置和 ${profileModels.length} 个模型已安全保存。`,
      });
    } catch (error) {
      setFeedback({ kind: "error", text: errorText(error) });
    } finally {
      setSaving(false);
    }
  }
  async function test() {
    if (!id) return;
    setFeedback({ kind: "info", text: "正在验证连接…" });
    try {
      await backend<void>("ModelFacade", "TestModelConnection", id);
      setFeedback({ kind: "ok", text: "连接成功，模型服务可访问。" });
    } catch (error) {
      setFeedback({ kind: "error", text: errorText(error) });
    }
  }
  async function remove() {
    if (
      !id ||
      !window.confirm(
        "删除该 API 配置及系统凭据？若聊天历史仍引用该配置，SciAide 会拒绝删除。",
      )
    )
      return;
    try {
      await backend<void>("ModelFacade", "DeleteModelProfile", id);
      setId("");
      await refresh();
      setFeedback({ kind: "ok", text: "模型配置已删除。" });
    } catch (error) {
      setFeedback({ kind: "error", text: errorText(error) });
    }
  }
  return (
    <div className="modal-backdrop">
      <section
        className="model-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="model-title"
      >
        <header>
          <div>
            <span className="dialog-icon gradient">
              <Icon name="model" />
            </span>
            <div>
              <p>MODEL GATEWAY</p>
              <h2 id="model-title">模型与 API</h2>
            </div>
          </div>
          <button className="close" onClick={close}>
            <Icon name="close" />
          </button>
        </header>
        <div className="settings-grid">
          <aside>
            <button
              className={`add-profile ${!id ? "selected" : ""}`}
              onClick={() => setId("")}
            >
              <Icon name="plus" /> 添加 API 配置
            </button>
            <div className="profile-caption">已保存</div>
            {profiles.map((profile) => (
              <button
                className={`profile-item ${profile.id === id ? "selected" : ""}`}
                onClick={() => setId(profile.id)}
                key={profile.id}
              >
                <span className="provider-logo">AI</span>
                <span>
                  <b>{profile.name}</b>
                  <small>
                    {
                      protocolLabels[
                        profile.apiProtocol ?? "openai_chat_completions"
                      ]
                    }{" "}
                    · {profile.models.length} 个模型
                  </small>
                </span>
                <i
                  className={
                    profile.secretConfigured ? "status-dot ready" : "status-dot"
                  }
                />
              </button>
            ))}
          </aside>
          <form onSubmit={(event) => void save(event)}>
            <section className="form-section">
              <div className="form-heading">
                <span>01</span>
                <div>
                  <h3>服务连接</h3>
                  <p>一个 Base URL 与 API Key 可关联多个模型</p>
                </div>
              </div>
              <label>
                接口协议
                <select
                  value={apiProtocol}
                  onChange={(event) => {
                    const next = event.target.value as APIProtocol;
                    setAPIProtocol(next);
                    setProfileModels((currentModels) =>
                      currentModels.map((model) => {
                        const inferred = inferredReasoningLevels(
                          next,
                          model.id,
                        );
                        return {
                          ...model,
                          reasoningLevels: inferred,
                          reasoningCapabilitySource: inferred.length
                            ? "inferred"
                            : "unsupported",
                          reasoningVerifiedLevels: [],
                          reasoningRejectedLevels: [],
                          reasoningControlUnsupported: false,
                          reasoningLastRequestedLevel: undefined,
                          reasoningLastResolvedLevel: undefined,
                          reasoningWireMode: undefined,
                        };
                      }),
                    );
                    setModels([]);
                    setFeedback(null);
                  }}
                >
                  <option value="openai_chat_completions">
                    OpenAI Chat Completions · /v1/chat/completions
                  </option>
                  <option value="openai_responses">
                    OpenAI Responses · /v1/responses
                  </option>
                  <option value="anthropic_messages">
                    Anthropic Messages · /v1/messages
                  </option>
                </select>
                <small className="field-help">
                  当前配置使用 {protocolLabels[apiProtocol]}。
                </small>
              </label>
              <div className="form-row two">
                <label>
                  配置名称
                  <input
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                    placeholder="例如：实验室模型服务"
                    required
                  />
                </label>
                <label>
                  API Key{" "}
                  <small>
                    {current?.secretConfigured
                      ? `已保存 ${current.secretMasked}`
                      : "本地服务可留空"}
                  </small>
                  <input
                    className="api-key-input"
                    type="password"
                    autoComplete="new-password"
                    value={apiKey}
                    onChange={(event) => setApiKey(event.target.value)}
                    placeholder={
                      current?.secretConfigured ? "留空保持现有密钥" : "sk-…"
                    }
                  />
                </label>
              </div>
              <label>
                Base URL
                <div className="endpoint-row">
                  <input
                    value={baseUrl}
                    onChange={(event) => {
                      setBaseUrl(event.target.value);
                      setModels([]);
                    }}
                    placeholder="https://api.openai.com/v1"
                    required
                  />
                  <button
                    type="button"
                    className="discover"
                    onClick={() => void discover()}
                    disabled={discovering || !baseUrl.trim()}
                  >
                    <Icon name="refresh" size={15} />
                    {discovering ? "获取中" : "获取模型"}
                  </button>
                </div>
                <small className="field-help">
                  模型发现将请求{" "}
                  <code>
                    {baseUrl.replace(/\/$/, "") || "{Base URL}"}/models
                  </code>
                  ；实际对话使用所选协议端点。
                </small>
                {apiProtocol === "anthropic_messages" && (
                  <small className="field-help protocol-note">
                    Anthropic 原生服务可能不提供 /v1/models，请手动填写 Model
                    ID；“测试连接”会验证 /v1/messages。
                  </small>
                )}
              </label>
            </section>
            <section className="form-section">
              <div className="form-heading">
                <span>02</span>
                <div>
                  <h3>可用模型</h3>
                  <p>勾选多个模型，并指定聊天默认模型；思考档位自动适配</p>
                </div>
              </div>
              {models.length > 0 && (
                <div className="model-browser">
                  <div className="model-search">
                    <Icon name="search" size={16} />
                    <input
                      value={modelSearch}
                      onChange={(event) => setModelSearch(event.target.value)}
                      placeholder={`搜索 ${models.length} 个模型`}
                    />
                  </div>
                  <div className="model-results">
                    {filteredModels.slice(0, 80).map((item) => {
                      const selected = profileModels.some(
                        (model) => model.id === item.id,
                      );
                      return (
                        <button
                          type="button"
                          className={selected ? "selected" : ""}
                          key={item.id}
                          onClick={() => toggleModel(item)}
                        >
                          <span className="checkbox">
                            {selected && <Icon name="check" size={11} />}
                          </span>
                          <b>{item.id}</b>
                          <small>{item.ownedBy || "OpenAI-compatible"}</small>
                          {selected && <span className="chosen">已选</span>}
                        </button>
                      );
                    })}
                    {filteredModels.length === 0 && <p>没有匹配的模型</p>}
                  </div>
                </div>
              )}
              <div className="manual-model">
                <input
                  value={manualModelId}
                  onChange={(event) => setManualModelId(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") {
                      event.preventDefault();
                      addManualModel();
                    }
                  }}
                  placeholder="手动输入 Model ID"
                />
                <button
                  type="button"
                  onClick={addManualModel}
                  disabled={!manualModelId.trim()}
                >
                  <Icon name="plus" size={14} /> 添加
                </button>
              </div>
              {profileModels.length > 0 && (
                <div className="selected-models">
                  <p>已选择 {profileModels.length} 个模型</p>
                  {profileModels.map((model) => (
                    <div className="selected-model-row" key={model.id}>
                      <button
                        type="button"
                        className={`default-model ${model.isDefault ? "active" : ""}`}
                        onClick={() => setDefaultModel(model.id)}
                        title="设为默认模型"
                      >
                        <span className="radio">
                          {model.isDefault && <i />}
                        </span>
                        <b>{model.id}</b>
                        {model.isDefault && <small>默认</small>}
                      </button>
                      <label
                        className="model-context-window"
                        title="模型原始上下文窗口。SciAide 在 90% 自动压缩，并保留额外请求余量。"
                      >
                        <span>{modelContextSummary(model)}</span>
                        <input
                          aria-label={`${model.id} 上下文窗口`}
                          type="number"
                          min={4096}
                          max={10000000}
                          step={1}
                          value={model.contextWindowTokens || defaultContextWindowTokens}
                          onChange={(event) =>
                            setModelContextWindow(model.id, Number(event.target.value))
                          }
                        />
                      </label>
                      <div
                        className="model-reasoning-auto"
                        title={modelReasoningSummary(model).title}
                      >
                        <Icon name="spark" size={11} />
                        <span>{modelReasoningSummary(model).label}</span>
                      </div>
                      <button
                        type="button"
                        className="remove-model"
                        onClick={() => toggleModel(model)}
                        title="移除模型"
                      >
                        <Icon name="close" size={13} />
                      </button>
                    </div>
                  ))}
                </div>
              )}
              <details>
                <summary>高级设置 · 自定义 Headers</summary>
                <label>
                  非敏感 Headers（JSON）
                  <input
                    value={headers}
                    onChange={(event) => setHeaders(event.target.value)}
                    placeholder='{"X-Workspace":"lab"}'
                  />
                  <small className="field-help">
                    Authorization、Cookie、Token 等敏感 Header 会被拒绝。
                  </small>
                </label>
              </details>
            </section>
            <div className="secret-note">
              <Icon name="shield" size={17} />
              <div>
                <b>同一配置只保存一份密钥</b>
                <span>
                  所有已选模型共用该连接；API Key 仅写入 Windows Credential
                  Manager。全局用量请从左侧“用量统计”查看。
                </span>
              </div>
            </div>
            {feedback && (
              <div className={`feedback ${feedback.kind}`}>
                {feedback.kind === "ok" && <Icon name="check" size={16} />}
                <span>{feedback.text}</span>
              </div>
            )}
            <footer className="modal-actions">
              {id && (
                <button
                  type="button"
                  className="danger"
                  onClick={() => void remove()}
                >
                  删除配置
                </button>
              )}
              <span />
              {id && (
                <button type="button" onClick={() => void test()}>
                  测试连接
                </button>
              )}
              <button
                className="primary"
                disabled={saving || profileModels.length === 0}
              >
                {saving ? "保存中…" : "保存配置"}
              </button>
            </footer>
          </form>
        </div>
      </section>
    </div>
  );
}
