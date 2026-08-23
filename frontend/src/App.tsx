import { CSSProperties, FormEvent, memo, ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { eventsOn, minimiseWindow, onFileDrop, quitApplication, setClipboardText, toggleMaximiseWindow } from "./lib/wailsRuntime";

type Project = { id: string; name: string; description: string; workspacePath: string; workspaceKind: "managed" | "external" };
type PermissionMode = "plan" | "full_access";
type ReasoningLevel = "low" | "medium" | "high" | "xhigh" | "max";
type APIProtocol = "openai_chat_completions" | "openai_responses" | "anthropic_messages";
type Conversation = { id: string; projectId: string; title: string; modelProfileId: string; modelId: string; permissionMode: PermissionMode; reasoningLevel: ReasoningLevel };
type AttachmentReference = { attachmentId: string; originalName: string; mimeType: string; format: string; sizeBytes: number; unitCount: number; truncated: boolean };
type MessagePart = { type: string; text?: string; payload?: AttachmentReference };
type Citation = { id: string; messageId: string; runId: string; toolCallId: string; projectId: string; reference: string; ordinal: number; indexVersionId: string; documentId: string; attachmentId: string; chunkId: string; sourceName: string; mimeType?: string; locator: string; title?: string; quote: string; quoteSha256: string; sourceStart: number; sourceEnd: number; createdAt: string };
type MessageReasoning = { status: string; requestedLevel: ReasoningLevel; resolvedLevel?: ReasoningLevel; observed: boolean; signatureObserved: boolean; tokens: number; summary?: string };
type Message = { id: string; runId?: string; role: "user" | "assistant" | "system" | "tool"; status: string; parts: MessagePart[]; citations?: Citation[]; reasoning?: MessageReasoning };
type Attachment = { id: string; projectId: string; originalName: string; mimeType: string; format: string; sizeBytes: number; sha256: string; status: "parsing" | "ready" | "failed"; unitCount: number; extractedRunes: number; truncated: boolean; errorMessage?: string };
type AttachmentImportBatch = { attachments: Attachment[]; errors: { path: string; message: string }[] };
type KnowledgeJob = { id: string; documentId: string; status: "queued" | "running" | "completed" | "failed" | "cancelled"; stage: "queued" | "loading" | "chunking" | "indexing" | "completed" | "failed" | "cancelled"; attemptCount: number; errorMessage?: string; createdAt: string; startedAt?: string; completedAt?: string; updatedAt: string };
type ParseDiagnostic = { format: string; quality: "good" | "warning" | "poor" | "unavailable"; summary: string; warnings: string[]; sizeBytes: number; unitCount: number; extractedRunes: number; truncated: boolean; pages?: number; textPages?: number; emptyPages?: number; sections?: number; headings?: number; tables?: number; sheets?: number; parser?: string };
type KnowledgeDocument = { id: string; projectId: string; attachmentId: string; indexVersionId: string; title: string; attachmentSha256: string; status: "pending" | "indexing" | "ready" | "failed"; parserSchemaVersion: number; chunkingVersion: string; chunkCount: number; errorMessage?: string; createdAt: string; indexedAt?: string; updatedAt: string; job?: KnowledgeJob; progress: number; diagnostic: ParseDiagnostic };
type EmbeddingConfig = { enabled: boolean; baseUrl: string; modelId: string; dimensions: number; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; lastTestedAt?: string; updatedAt: string };
type ProfileModel = { id: string; ownedBy?: string; enabled: boolean; isDefault: boolean; contextWindowTokens: number; autoCompactTokenLimit: number; contextWindowSource: "fallback" | "provider" | "manual" | "builtin"; reasoningLevels: ReasoningLevel[]; reasoningCapabilitySource?: string; reasoningVerifiedLevels?: ReasoningLevel[]; reasoningRejectedLevels?: ReasoningLevel[]; reasoningControlUnsupported?: boolean; reasoningLastRequestedLevel?: ReasoningLevel; reasoningLastResolvedLevel?: ReasoningLevel; reasoningWireMode?: string };
type Profile = { id: string; name: string; apiProtocol: APIProtocol; baseUrl: string; modelId: string; models: ProfileModel[]; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; customHeaders: Record<string, string>; enabled: boolean; isDefault: boolean };
type AvailableModel = { id: string; ownedBy?: string; contextWindowTokens?: number; autoCompactTokenLimit?: number; contextWindowSource?: "fallback" | "provider" | "manual" | "builtin"; reasoningLevels?: ReasoningLevel[]; reasoningCapabilitySource?: string };
type Run = { id: string; conversationId: string; assistantMessageId: string; modelProfileId: string; modelId: string; status: string; errorCode?: string; errorMessage?: string; errorDetails?: string; apiProtocol: APIProtocol; inputTokens: number; freshInputTokens: number; outputTokens: number; reasoningTokens: number; reasoningObserved: boolean; reasoningSignatureObserved: boolean; reasoningSummary?: string; cachedInputTokens: number; cacheWriteTokens: number; cacheReportedTurns: number; cacheHitTurns: number; permissionMode: PermissionMode; requestedReasoningLevel: ReasoningLevel; resolvedReasoningLevel?: ReasoningLevel; contextWindowTokens: number; contextBudgetTokens: number; autoCompactTokenLimit: number; contextWindowSource: "fallback" | "provider" | "manual" | "builtin"; contextCompacted: boolean; createdAt: string; startedAt?: string; completedAt?: string };
type RunStep = { runId: string; turnIndex: number; commentary?: string; reasoningSummary?: string; reasoningObserved: boolean; reasoningSignatureObserved: boolean; createdAt: string; completedAt: string };
type RetryStatus = { phase: "request" | "stream"; attempt: number; maxAttempts: number; delayMillis: number; message: string };
type ContextCompactionResult = { revision: number; throughMessageId: string; sourceMessageCount: number; sourceEstimatedTokens: number; passes: number; complete: boolean };
type UsageQuery = { startDate?: string; endDate?: string; startTime?: string; endTime?: string; modelProfileId?: string; modelId?: string };
type UsageSummary = { runCount: number; modelTurns: number; requestCount: number; successfulRequests: number; failedRequests: number; successRate: number; freshInputTokens: number; outputTokens: number; reasoningTokens: number; cacheReadTokens: number; cacheCreationTokens: number; realTotalTokens: number; cacheReportedTurns: number; cacheHitTurns: number; cacheHitRate: number; cacheDataAvailable: boolean };
type DailyUsage = UsageSummary & { date: string };
type ModelUsage = UsageSummary & { modelProfileId: string; profileName: string; modelId: string };
type UsageDashboardData = { query: UsageQuery; summary: UsageSummary; daily: DailyUsage[]; models: ModelUsage[] };
type RequestUsage = { id: string; runId: string; turnIndex: number; requestKind: "conversation" | "compaction" | "image_probe" | "multimodal_fallback" | "legacy"; modelProfileId: string; profileName: string; modelId: string; apiProtocol: APIProtocol; inputTokens: number; freshInputTokens: number; outputTokens: number; reasoningTokens: number; cachedInputTokens: number; cacheWriteTokens: number; cacheDetailsReported: boolean; statusCode: number; errorCode?: string; errorMessage?: string; firstTokenMillis?: number; isStreaming: boolean; startedAt: string; completedAt: string; durationMillis: number };
type UsageRequestPage = { items: RequestUsage[]; total: number; offset: number; limit: number; query: UsageQuery & { statusCode?: number; offset: number; limit: number } };
type PermissionRequirement = { kind: string; resource: string };
type ToolResult = { status: string; text: string; truncated: boolean; meta: { durationMillis?: number; originalBytes?: number } };
type ToolCall = { id: string; runId: string; toolName: string; toolVersion: string; arguments: unknown; status: string; risk: string; permissions: PermissionRequirement[]; errorMessage?: string; result?: ToolResult; createdAt: string; startedAt?: string; completedAt?: string };
type Approval = { id: string; runId: string; toolCallId: string; toolName: string; toolVersion: string; permissionKind: string; resource: string; risk: string; status: string; reason: string };
type RunSnapshot = { sequence: number; run: Run; messages: Message[]; toolCalls: ToolCall[]; runSteps: RunStep[]; pendingApprovals: Approval[] };
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
type EnableAllProjectSkillsResult = { enabled: number; alreadyEnabled: number; skipped: number };
type VisionFallbackChannel = { id: string; name: string; baseUrl?: string; modelId: string; apiProtocol: APIProtocol; priority: number; enabled: boolean; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; maxTokens: number };
type Envelope = { aggregateId: string; sequence: number; type: string; payload: Record<string, unknown> };
type CreateDialog = { kind: "project" | "conversation"; title: string; description: string; workspacePath: string } | null;
type IconName = "spark" | "plus" | "chat" | "settings" | "shield" | "model" | "send" | "stop" | "search" | "refresh" | "folder" | "check" | "close" | "back" | "trash" | "tool" | "server" | "chart" | "skill" | "paperclip" | "library" | "copy";
type SlashCommandID = "mcp" | "skill" | "knowledge" | "compact" | "model" | "reasoning" | "permission" | "status" | "usage" | "new" | "help";
type SlashCommand = { id: SlashCommandID; name: string; title: string; description: string; icon: IconName; enabled: boolean; disabledReason?: string; state?: string; stateKind?: "on" | "off" | "loading" };
type SlashPanelMode = "mcp" | "mcp-detail" | "skill" | "knowledge" | "model" | "reasoning" | "permission" | "status" | "usage";
type SlashSkillItem = { id: string; name: string; version: string; description: string; enabled: boolean; available: boolean; reason?: string };
type ContentRevealJob = { messageId: string; conversationId: string; characters: string[]; index: number; visible: string; credit: number; lastTick: number; pauseUntil: number };

const emptyToolCalls: ToolCall[] = [];
const emptyApprovals: Approval[] = [];
const emptyRunSteps: RunStep[] = [];

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

async function copyToClipboard(text: string) {
  try {
    if (await setClipboardText(text)) return;
  } catch {
    // Browser clipboard remains available when the native bridge rejects.
  }
  try {
    await navigator.clipboard.writeText(text);
    return;
  } catch {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.opacity = "0";
    document.body.appendChild(textarea);
    textarea.select();
    const copied = document.execCommand("copy");
    textarea.remove();
    if (!copied) throw new Error("浏览器未允许复制文本");
  }
}

const textOf = (message: Message) => message.parts.filter((part) => part.type === "text").map((part) => part.text ?? "").join("");
const attachmentsOf = (message: Message) => message.parts.filter((part) => part.type === "media" && part.payload?.attachmentId).map((part) => part.payload as AttachmentReference);
const attachmentSummary = (item: AttachmentReference | Attachment) => item.format === "image" ? `${item.mimeType.replace("image/", "").toUpperCase()} 图片 · ${fileSize(item.sizeBytes)}` : `${item.format.toUpperCase()} · ${fileSize(item.sizeBytes)} · ${item.unitCount} 个可读单元${item.truncated ? " · 已截断" : ""}`;
const fileSize = (bytes: number) => bytes < 1024 ? `${bytes} B` : bytes < 1024 * 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
const first = <T,>(items: T[]) => items[0];
const normalizeMCPCapabilities = (value: Partial<MCPCapabilities> | null | undefined): MCPCapabilities => ({
  protocolVersion: value?.protocolVersion,
  serverVersion: value?.serverVersion,
  tools: Array.isArray(value?.tools) ? value.tools : [],
  resources: Array.isArray(value?.resources) ? value.resources : [],
  prompts: Array.isArray(value?.prompts) ? value.prompts : [],
});
const modelKey = (profileId: string, modelId: string) => `${profileId}\t${modelId}`;
const splitModelKey = (value: string): [string, string] => { const index = value.indexOf("\t"); return index < 0 ? ["", ""] : [value.slice(0, index), value.slice(index + 1)]; };
const reasoningLevels: ReasoningLevel[] = ["low", "medium", "high", "xhigh", "max"];
const reasoningDescriptions: Record<ReasoningLevel, string> = {
  low: "较快，适合简单整理与直接问答",
  medium: "速度与推理深度平衡",
  high: "适合复杂分析和多步骤任务",
  xhigh: "更深入推理，耗时和用量更高",
  max: "请求模型支持的最高思考强度",
};
const defaultContextWindowTokens = 200_000;
const automaticCompactLimit = (windowTokens: number) => Math.floor(windowTokens * 0.9);
const mcpRuntimeState = (server: MCPServer): { label: string; kind: "on" | "off" | "loading"; active: boolean } => {
  if (server.status === "ready") return { label: "已启动", kind: "on", active: true };
  if (server.status === "degraded") return { label: "已启动 · 部分异常", kind: "on", active: true };
  if (server.status === "starting" || server.status === "initializing") return { label: "正在启动", kind: "loading", active: true };
  if (server.status === "stopping") return { label: "正在关闭", kind: "loading", active: true };
  if (server.status === "failed") return { label: "启动失败", kind: "off", active: false };
  if (server.status === "disabled" || !server.enabled) return { label: "已禁用", kind: "off", active: false };
  return { label: "已关闭", kind: "off", active: false };
};
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

const reasoningProjectionKey = (value?: MessageReasoning) => value ? [value.status, value.requestedLevel, value.resolvedLevel, value.observed, value.signatureObserved, value.tokens, value.summary].join("\t") : "";
const citationProjectionKey = (values?: Citation[]) => (values ?? []).map((value) => `${value.id}:${value.reference}:${value.quoteSha256}`).join("\t");
const replaceMessageText = (message: Message, text: string): Message => ({ ...message, parts: [{ type: "text", text }, ...message.parts.filter((part) => part.type !== "text")] });
const mergeSnapshotMessages = (current: Message[], snapshot: Message[], protectedTextIds: ReadonlySet<string> = new Set()): Message[] => {
  const currentByID = new Map(current.map((message) => [message.id, message]));
  const merged = orderedMessages(snapshot.map((message) => {
    const live = currentByID.get(message.id);
    if (!live) return message;
    const liveText = textOf(live); const snapshotText = textOf(message);
    if (protectedTextIds.has(message.id)) return replaceMessageText(message, liveText);
    if (message.status === "streaming" && liveText.length > snapshotText.length) return live;
    const unchanged = liveText === snapshotText
      && live.role === message.role
      && live.runId === message.runId
      && live.status === message.status
      && live.parts.length === message.parts.length
      && reasoningProjectionKey(live.reasoning) === reasoningProjectionKey(message.reasoning)
      && citationProjectionKey(live.citations) === citationProjectionKey(message.citations);
    return unchanged ? live : message;
  }));
  return current.length === merged.length && merged.every((message, index) => message === current[index]) ? current : merged;
};

function Icon({ name, size = 18 }: { name: IconName; size?: number }) {
  const paths: Record<typeof name, ReactNode> = {
    spark: <><path d="m12 2 1.35 4.15L17.5 7.5l-4.15 1.35L12 13l-1.35-4.15L6.5 7.5l4.15-1.35L12 2Z"/><path d="m5 14 .8 2.2L8 17l-2.2.8L5 20l-.8-2.2L2 17l2.2-.8L5 14Z"/></>,
    plus: <><path d="M12 5v14M5 12h14"/></>, chat: <path d="M21 15a4 4 0 0 1-4 4H8l-5 3V7a4 4 0 0 1 4-4h10a4 4 0 0 1 4 4v8Z"/>,
    settings: <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.09A1.7 1.7 0 0 0 9 19.36a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.63 15 1.7 1.7 0 0 0 3.08 14H3v-4h.09A1.7 1.7 0 0 0 4.64 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 9 4.63h.01A1.7 1.7 0 0 0 10 3.08V3h4v.09A1.7 1.7 0 0 0 15 4.64a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.37 9v.01A1.7 1.7 0 0 0 20.92 10H21v4h-.09A1.7 1.7 0 0 0 19.4 15Z"/></>,
    shield: <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/>, model: <><rect x="3" y="3" width="18" height="18" rx="5"/><path d="M8 9h8M8 12h5M8 15h7"/></>,
    send: <><path d="m22 2-7 20-4-9-9-4 20-7Z"/><path d="M22 2 11 13"/></>, stop: <rect x="6" y="6" width="12" height="12" rx="2"/>, search: <><circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/></>,
    refresh: <><path d="M20 11a8 8 0 1 0-2.34 5.66"/><path d="M20 4v7h-7"/></>, folder: <path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6Z"/>,
    check: <path d="m5 12 4 4L19 6"/>, close: <><path d="m6 6 12 12M18 6 6 18"/></>, back: <><path d="m15 18-6-6 6-6"/><path d="M9 12h10"/></>, trash: <><path d="M3 6h18M8 6V4h8v2M19 6l-1 15H6L5 6M10 11v5M14 11v5"/></>, tool: <><path d="M14.7 6.3a4 4 0 0 0-5 5L3 18l3 3 6.7-6.7a4 4 0 0 0 5-5l-2.2 2.2-3-3 2.2-2.2Z"/></>, server: <><rect x="4" y="3" width="16" height="7" rx="2"/><rect x="4" y="14" width="16" height="7" rx="2"/><path d="M8 6.5h.01M8 17.5h.01M12 6.5h5M12 17.5h5"/></>,
    chart: <><path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/></>,
    skill: <><path d="M7 3h8l4 4v14H7a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Z"/><path d="M14 3v5h5M9 12h6M9 16h6"/></>,
    paperclip: <path d="m21.4 11.6-8.9 8.9a6 6 0 0 1-8.5-8.5l9.6-9.6a4 4 0 0 1 5.7 5.7l-9.6 9.6a2 2 0 1 1-2.8-2.8l8.9-8.9"/>,
    library: <><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2Z"/><path d="M8 7h8M8 11h6"/></>,
    copy: <><rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/></>,
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
  const [retryStatus, setRetryStatus] = useState<RetryStatus | null>(null);
  const [toolCalls, setToolCalls] = useState<ToolCall[]>([]);
  const [runSteps, setRunSteps] = useState<RunStep[]>([]);
  const [pendingApprovals, setPendingApprovals] = useState<Approval[]>([]);
  const [revealingMessageIds, setRevealingMessageIds] = useState<Set<string>>(() => new Set());
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
  const [mcpServers, setMcpServers] = useState<MCPServer[] | null>(null);
  const [mcpStatusLoading, setMcpStatusLoading] = useState(false);
  const [mcpStatusError, setMcpStatusError] = useState(false);
  const [slashSelected, setSlashSelected] = useState(0);
  const [slashPanel, setSlashPanel] = useState<SlashPanelMode | null>(null);
  const [slashPanelLoading, setSlashPanelLoading] = useState(false);
  const [slashSkills, setSlashSkills] = useState<SlashSkillItem[]>([]);
  const [slashKnowledge, setSlashKnowledge] = useState<KnowledgeDocument[]>([]);
  const [slashVision, setSlashVision] = useState<VisionFallbackChannel[]>([]);
  const [slashStatusErrors, setSlashStatusErrors] = useState<string[]>([]);
  const [slashUsage, setSlashUsage] = useState<UsageDashboardData | null>(null);
  const [slashMCPServer, setSlashMCPServer] = useState<MCPServer | null>(null);
  const [slashMCPCapabilities, setSlashMCPCapabilities] = useState<MCPCapabilities | null>(null);
  const [slashMCPAction, setSlashMCPAction] = useState(false);
  const [compacting, setCompacting] = useState(false);
  const [createDialog, setCreateDialog] = useState<CreateDialog>(null);
  const [busy, setBusy] = useState(false);
  const activeRunRef = useRef<Run | null>(null);
  const busyRef = useRef(false);
  const conversationIdRef = useRef("");
  const messagesRef = useRef<Message[]>([]);
  const modelSelectionRef = useRef({ profileId: "", modelId: "" });
  const restoringModelSelectionRef = useRef(false);
  const modelSelectionSaveRef = useRef<Promise<void>>(Promise.resolve());
  const chatRef = useRef<HTMLElement | null>(null);
  const composerInputRef = useRef<HTMLTextAreaElement | null>(null);
  const slashMenuRef = useRef<HTMLDivElement | null>(null);
  const pendingContentDeltasRef = useRef<Map<string, string>>(new Map());
  const contentFrameRef = useRef<number | null>(null);
  const contentRevealJobsRef = useRef<Map<string, ContentRevealJob>>(new Map());
  const contentRevealTimerRef = useRef<number | null>(null);
  const contentRevealTickRef = useRef<() => void>(() => undefined);
  const autoFollowRef = useRef(true);
  const runSequenceRef = useRef<Map<string, number>>(new Map());
  activeRunRef.current = activeRun;
  busyRef.current = busy;
  conversationIdRef.current = conversationId;
  messagesRef.current = messages;
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
  const loadMCPStatus = useCallback(async () => {
    setMcpStatusLoading(true); setMcpStatusError(false);
    try { setMcpServers(await backend<MCPServer[]>("MCPFacade", "ListMCPServers")); }
    catch { setMcpStatusError(true); }
    finally { setMcpStatusLoading(false); }
  }, []);
  const loadConversations = useCallback(async (selectedProject: string) => {
    if (!selectedProject) { setConversations([]); setConversationId(""); return; }
    const values = await backend<Conversation[]>("ConversationFacade", "ListConversations", selectedProject);
    setConversations(values); setConversationId((current) => values.some((item) => item.id === current) ? current : first(values)?.id || "");
  }, []);
  const loadMessages = useCallback(async (selectedConversation: string) => {
    if (!selectedConversation) { setMessages([]); return; }
    const loaded = orderedMessages(await backend<Message[]>("ConversationFacade", "ListMessages", selectedConversation));
    setMessages((current) => mergeSnapshotMessages(current, loaded, new Set(contentRevealJobsRef.current.keys())));
  }, []);
  const flushContentDeltas = useCallback(() => {
    contentFrameRef.current = null;
    const pending = pendingContentDeltasRef.current;
    pendingContentDeltasRef.current = new Map();
    if (!pending.size) return;
    setMessages((current) => current.map((message) => {
      const delta = pending.get(message.id);
      return delta ? replaceMessageText(message, textOf(message) + delta) : message;
    }));
  }, []);
  const queueContentDelta = useCallback((messageId: string, delta: string) => {
    if (!messageId || !delta) return;
    const pending = pendingContentDeltasRef.current;
    pending.set(messageId, (pending.get(messageId) ?? "") + delta);
    if (contentFrameRef.current === null) contentFrameRef.current = window.requestAnimationFrame(flushContentDeltas);
  }, [flushContentDeltas]);
  const discardContentDeltas = useCallback(() => {
    pendingContentDeltasRef.current.clear();
    if (contentFrameRef.current !== null) window.cancelAnimationFrame(contentFrameRef.current);
    contentFrameRef.current = null;
  }, []);
  const discardContentReveals = useCallback(() => {
    contentRevealJobsRef.current.clear();
    if (contentRevealTimerRef.current !== null) window.clearInterval(contentRevealTimerRef.current);
    contentRevealTimerRef.current = null;
    setRevealingMessageIds(new Set());
  }, []);
  const resetContentAttempt = useCallback((messageId: string) => {
    messageId = messageId.trim();
    if (!messageId) return;
    pendingContentDeltasRef.current.delete(messageId);
    if (!pendingContentDeltasRef.current.size && contentFrameRef.current !== null) {
      window.cancelAnimationFrame(contentFrameRef.current);
      contentFrameRef.current = null;
    }
    contentRevealJobsRef.current.delete(messageId);
    if (!contentRevealJobsRef.current.size && contentRevealTimerRef.current !== null) {
      window.clearInterval(contentRevealTimerRef.current);
      contentRevealTimerRef.current = null;
    }
    setRevealingMessageIds((current) => {
      if (!current.has(messageId)) return current;
      const next = new Set(current); next.delete(messageId); return next;
    });
    setMessages((current) => current.map((message) => message.id === messageId ? replaceMessageText(message, "") : message));
  }, []);
  const completeContentReveals = useCallback(() => {
    const jobs = [...contentRevealJobsRef.current.values()];
    if (jobs.length) {
      const completed = new Map(jobs.map((job) => [job.messageId, job.characters.join("")]));
      setMessages((current) => current.map((message) => completed.has(message.id) ? replaceMessageText(message, completed.get(message.id) ?? "") : message));
    }
    discardContentReveals();
  }, [discardContentReveals]);
  const ensureContentRevealTimer = useCallback(() => {
    if (contentRevealTimerRef.current !== null) return;
    contentRevealTimerRef.current = window.setInterval(() => contentRevealTickRef.current(), 32);
  }, []);
  const beginContentReveal = useCallback((messageId: string, text: string) => {
    messageId = messageId.trim();
    if (!messageId) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      setMessages((current) => current.map((message) => message.id === messageId ? replaceMessageText(message, text) : message));
      return;
    }
    const characters = Array.from(text);
    const currentMessage = messagesRef.current.find((message) => message.id === messageId);
    const currentText = currentMessage ? textOf(currentMessage) : "";
    // A terminal snapshot can win the race with content.completed. Reset a
    // fully projected snapshot so the answer still reveals exactly once.
    const visible = currentText !== text && text.startsWith(currentText) ? currentText : "";
    const index = Array.from(visible).length;
    if (index >= characters.length) return;
    contentRevealJobsRef.current.set(messageId, { messageId, conversationId: conversationIdRef.current, characters, index, visible, credit: 0, lastTick: performance.now(), pauseUntil: 0 });
    setMessages((current) => current.map((message) => message.id === messageId ? replaceMessageText(message, visible) : message));
    setRevealingMessageIds((current) => new Set(current).add(messageId));
    ensureContentRevealTimer();
  }, [ensureContentRevealTimer]);
  contentRevealTickRef.current = () => {
    const jobs = contentRevealJobsRef.current;
    if (!jobs.size) {
      if (contentRevealTimerRef.current !== null) window.clearInterval(contentRevealTimerRef.current);
      contentRevealTimerRef.current = null;
      return;
    }
    const now = performance.now();
    const updates = new Map<string, string>();
    const completed: ContentRevealJob[] = [];
    for (const job of jobs.values()) {
      if (now < job.pauseUntil) continue;
      const elapsed = Math.max(0, now - job.lastTick);
      job.lastTick = now;
      const targetSeconds = Math.min(9, Math.max(1.8, job.characters.length / 60));
      const rate = job.characters.length / targetSeconds;
      job.credit += elapsed * rate / 1000;
      const count = Math.min(job.characters.length - job.index, Math.floor(job.credit));
      if (count <= 0) continue;
      job.credit -= count;
      const next = job.index + count;
      const fragment = job.characters.slice(job.index, next).join("");
      job.visible += fragment;
      job.index = next;
      updates.set(job.messageId, job.visible);
      if (/[。！？.!?\n]$/.test(fragment) && next < job.characters.length) job.pauseUntil = now + 70;
      if (next >= job.characters.length) {
        jobs.delete(job.messageId);
        completed.push(job);
      }
    }
    if (updates.size) setMessages((current) => current.map((message) => updates.has(message.id) ? replaceMessageText(message, updates.get(message.id) ?? "") : message));
    if (completed.length) {
      const completedIds = new Set(completed.map((job) => job.messageId));
      setRevealingMessageIds((current) => new Set([...current].filter((id) => !completedIds.has(id))));
      for (const job of completed) {
        if (job.conversationId !== conversationIdRef.current) continue;
        void backend<Message[]>("ConversationFacade", "ListMessages", job.conversationId).then((loaded) => {
          if (job.conversationId !== conversationIdRef.current) return;
          setMessages((current) => mergeSnapshotMessages(current, orderedMessages(loaded), new Set(contentRevealJobsRef.current.keys())));
        }).catch(() => undefined);
      }
    }
    if (!jobs.size && contentRevealTimerRef.current !== null) {
      window.clearInterval(contentRevealTimerRef.current);
      contentRevealTimerRef.current = null;
    }
  };
  const applySnapshot = useCallback((snapshot: RunSnapshot) => {
    if (snapshot.run.conversationId !== conversationIdRef.current) return;
    const knownSequence = runSequenceRef.current.get(snapshot.run.id) ?? 0;
    if (snapshot.sequence > 0 && snapshot.sequence < knownSequence) return;
    if (snapshot.sequence > knownSequence) runSequenceRef.current.set(snapshot.run.id, snapshot.sequence);
    const terminal = ["completed", "failed", "cancelled", "interrupted"].includes(snapshot.run.status);
    setActiveRun(snapshot.run);
    setToolCalls(snapshot.toolCalls ?? []);
    setRunSteps(snapshot.runSteps ?? []);
    setPendingApprovals(snapshot.pendingApprovals ?? []);
    setMessages((current) => mergeSnapshotMessages(current, snapshot.messages, new Set(contentRevealJobsRef.current.keys())));
    if (terminal) setRetryStatus(null);
    setBusy(!terminal);
  }, []);

  useEffect(() => { Promise.all([loadProjects(), loadProfiles()]).catch((error: unknown) => setNotice(errorText(error))); }, [loadProfiles, loadProjects]);
  useEffect(() => { loadConversations(projectId).catch((error: unknown) => setNotice(errorText(error))); }, [loadConversations, projectId]);
  useEffect(() => {
    discardContentDeltas(); discardContentReveals(); autoFollowRef.current = true; runSequenceRef.current.clear();
    setActiveRun(null); setRetryStatus(null); setToolCalls([]); setRunSteps([]); setPendingApprovals([]); setPendingAttachments([]); setBusy(false);
    if (!conversationId) { setMessages([]); return; }
    Promise.all([
      loadMessages(conversationId),
      backend<RunSnapshot | null>("ChatFacade", "GetLatestRunSnapshot", conversationId).then((snapshot) => { if (snapshot) applySnapshot(snapshot); }),
    ]).catch((error: unknown) => setNotice(errorText(error)));
  }, [applySnapshot, conversationId, discardContentDeltas, discardContentReveals, loadMessages]);
  useLayoutEffect(() => {
    const chat = chatRef.current;
    if (chat && autoFollowRef.current) chat.scrollTop = chat.scrollHeight;
  }, [messages, activeRun?.reasoningSummary, retryStatus?.attempt, runSteps.length, toolCalls.length, pendingApprovals.length]);
  useEffect(() => onFileDrop((paths) => {
    if (projectId && conversationId && paths.length) void importDroppedDocuments(paths);
  }), [projectId, conversationId, importingAttachments]);

  useEffect(() => {
    const unsubscribe = eventsOn<Envelope>("sciaide:run-event", (event) => {
      const run = activeRunRef.current;
      if (!run || event.aggregateId !== run.id) {
        if (busyRef.current && event.type === "run.retrying" && event.payload.retry) setRetryStatus(event.payload.retry as unknown as RetryStatus);
        if (busyRef.current && event.type === "run.retry.recovered") setRetryStatus(null);
        return;
      }
      const knownSequence = runSequenceRef.current.get(run.id) ?? 0;
      const stale = event.sequence > 0 && event.sequence <= knownSequence;
      if (event.sequence > knownSequence) runSequenceRef.current.set(run.id, event.sequence);
      if (stale && event.type !== "run.retrying" && event.type !== "run.retry.recovered") return;
      if (event.type === "content.delta") {
        queueContentDelta(String(event.payload.messageId ?? ""), String(event.payload.delta ?? ""));
      }
      if (event.type === "content.completed") {
        const messageId = String(event.payload.messageId ?? ""); const text = String(event.payload.text ?? "");
        pendingContentDeltasRef.current.delete(messageId);
        beginContentReveal(messageId, text);
      }
      if (event.type === "activity.completed" && event.payload.step) {
        const step = event.payload.step as RunStep;
        setRunSteps((current) => [...current.filter((item) => item.turnIndex !== step.turnIndex), step].sort((left, right) => left.turnIndex - right.turnIndex));
      }
      if (event.type === "run.retrying" && event.payload.retry) {
        resetContentAttempt(String(event.payload.messageId ?? run.assistantMessageId ?? ""));
        setRetryStatus(event.payload.retry as unknown as RetryStatus);
      }
      if (event.type === "run.retry.recovered") setRetryStatus(null);
      if (event.type.startsWith("run.") && event.payload.run) setActiveRun(event.payload.run as Run);
      if (event.type.startsWith("tool.") || event.type.startsWith("approval.")) {
        backend<RunSnapshot>("ChatFacade", "GetRunSnapshot", run.id).then(applySnapshot).catch(() => undefined);
      }
      if (["run.completed", "run.failed", "run.cancelled", "run.interrupted"].includes(event.type)) {
        discardContentDeltas(); setRetryStatus(null); setBusy(false);
        const completedRun = event.payload.run as Run | undefined;
        if (!completedRun?.assistantMessageId || !contentRevealJobsRef.current.has(completedRun.assistantMessageId)) loadMessages(conversationId).catch(() => undefined);
        loadProfiles().catch(() => undefined);
      }
    });
    return () => { unsubscribe(); discardContentDeltas(); discardContentReveals(); };
  }, [applySnapshot, beginContentReveal, conversationId, discardContentDeltas, discardContentReveals, loadMessages, loadProfiles, queueContentDelta, resetContentAttempt]);

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
    const localCommand = slashCommands.find((item) => `/${item.name}` === text.toLowerCase());
    if (localCommand) {
      await executeSlashCommand(localCommand);
      return;
    }
    if ((!text && pendingAttachments.length === 0) || !conversationId || !profileId || !modelId) return;
    completeContentReveals();
    const runToSteer = busy && activeRun?.conversationId === conversationId ? activeRun : null;
    const submittedAttachments = pendingAttachments;
    autoFollowRef.current = true;
    setNotice(""); setInput(""); setPendingAttachments([]); setRetryStatus(null); setBusy(true);
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

  async function readSlashSkills() {
    const installed = await backend<InstalledSkill[]>("SkillFacade", "ListInstalledSkills");
    const links = projectId ? await backend<ProjectSkillView[]>("SkillFacade", "ListProjectSkills", projectId) : [];
    const linked = new Map(links.map((item) => [item.skillId, item]));
    const latest = new Map<string, InstalledSkill>();
    for (const item of installed) if (!latest.has(item.manifest.id)) latest.set(item.manifest.id, item);
    const ids = new Set([...latest.keys(), ...linked.keys()]);
    return [...ids].map((id) => {
      const link = linked.get(id); const item = link?.skill ?? latest.get(id);
      return { id, name: item?.manifest.name ?? id, version: link?.version ?? item?.manifest.version ?? "", description: item?.manifest.description ?? "", enabled: Boolean(link?.enabled), available: item?.availability === "available" && item.integrity === "valid", reason: item?.availabilityReason || item?.integrityError };
    }).sort((left, right) => Number(right.enabled) - Number(left.enabled) || left.name.localeCompare(right.name, "zh-CN"));
  }

  async function loadSlashSkills() {
    setSlashPanelLoading(true);
    try { setSlashSkills(await readSlashSkills()); }
    catch (error) { setNotice(errorText(error)); setSlashSkills([]); }
    finally { setSlashPanelLoading(false); }
  }

  async function loadSlashKnowledge() {
    if (!projectId) return;
    setSlashPanelLoading(true);
    try { setSlashKnowledge(await backend<KnowledgeDocument[]>("KnowledgeFacade", "ListDocuments", projectId)); }
    catch (error) { setNotice(errorText(error)); setSlashKnowledge([]); }
    finally { setSlashPanelLoading(false); }
  }

  async function loadSlashUsage() {
    setSlashPanelLoading(true);
    try { setSlashUsage(await backend<UsageDashboardData>("ChatFacade", "GetUsageDashboard", {})); }
    catch (error) { setNotice(errorText(error)); setSlashUsage(null); }
    finally { setSlashPanelLoading(false); }
  }

  async function loadSlashStatus() {
    setSlashPanelLoading(true); setSlashStatusErrors([]); setMcpStatusLoading(true); setMcpStatusError(false);
    const errors: string[] = [];
    const [servers, skills, documents, vision] = await Promise.all([
      backend<MCPServer[]>("MCPFacade", "ListMCPServers").catch(() => { errors.push("MCP"); return null; }),
      readSlashSkills().catch(() => { errors.push("Skills"); return null; }),
      (projectId ? backend<KnowledgeDocument[]>("KnowledgeFacade", "ListDocuments", projectId) : Promise.resolve([])).catch(() => { errors.push("知识库"); return null; }),
      backend<VisionFallbackChannel[]>("ModelFacade", "ListVisionFallbackChannels").catch(() => { errors.push("识图兜底"); return null; }),
    ]);
    if (servers) setMcpServers(servers); else { setMcpServers(null); setMcpStatusError(true); }
    setSlashSkills(skills ?? []);
    setSlashKnowledge(documents ?? []);
    setSlashVision(vision ?? []);
    setSlashStatusErrors(errors); setMcpStatusLoading(false); setSlashPanelLoading(false);
  }

  async function openSlashMCPServer(server: MCPServer) {
    setSlashMCPServer(server); setSlashMCPCapabilities(null); setSlashPanel("mcp-detail"); setSlashSelected(0);
    if (server.status !== "ready" && server.status !== "degraded") return;
    setSlashPanelLoading(true);
    try { setSlashMCPCapabilities(normalizeMCPCapabilities(await backend<MCPCapabilities>("MCPFacade", "GetMCPCapabilities", server.id))); }
    catch (error) { setNotice(errorText(error)); }
    finally { setSlashPanelLoading(false); }
  }

  async function toggleSlashMCPServer(server: MCPServer) {
    if (slashMCPAction) return;
    if (server.status === "starting" || server.status === "initializing" || server.status === "stopping") { setNotice("MCP Server 正在切换状态，请稍候。"); return; }
    const active = server.status === "ready" || server.status === "degraded";
    if (!active && (!server.enabled || server.trust !== "user_trusted")) { setNotice("请先在 MCP 配置中启用并信任此 Server。"); return; }
    setSlashMCPAction(true);
    try {
      const updated = await backend<MCPServer>("MCPFacade", active ? "DisconnectMCPServer" : "ConnectMCPServer", server.id);
      setMcpServers((current) => current?.map((item) => item.id === updated.id ? updated : item) ?? [updated]);
      await openSlashMCPServer(updated);
    } catch (error) { setNotice(errorText(error)); }
    finally { setSlashMCPAction(false); }
  }

  const selectedProject = projects.find((item) => item.id === projectId);
  const selectedConversation = conversations.find((item) => item.id === conversationId);
  const selectedProfile = profiles.find((item) => item.id === profileId);
  const selectedModel = selectedProfile?.models.find((item) => item.id === modelId);
  const selectableModels = useMemo(() => profiles.filter((profile) => profile.enabled).flatMap((profile) => profile.models.filter((model) => model.enabled).map((model) => ({ profile, model }))), [profiles]);
  const selectedModelKey = profileId && modelId ? modelKey(profileId, modelId) : "";
  const usage = useMemo(() => activeRun ? `${activeRun.inputTokens} 输入 · ${activeRun.outputTokens} 输出${activeRun.reasoningTokens > 0 ? ` · ${activeRun.reasoningTokens} 推理` : ""}${activeRun.cacheReportedTurns > 0 ? ` · ${activeRun.cachedInputTokens} 缓存命中` : ""} tokens` : "", [activeRun]);
  const reasoning = reasoningDisplay(selectedConversation?.reasoningLevel ?? "medium", selectedModel, activeRun, profileId, modelId);
  const slashTyping = input.length > 0 && /^\/[^\s]*$/u.test(input);
  const slashMenuOpen = slashPanel !== null || slashTyping;
  const slashQuery = slashPanel === null && slashTyping ? input.slice(1).toLowerCase() : "";
  const activeMCPCount = mcpServers?.filter((item) => item.status === "ready" || item.status === "degraded").length ?? 0;
  const transitioningMCPCount = mcpServers?.filter((item) => item.status === "starting" || item.status === "initializing" || item.status === "stopping").length ?? 0;
  const enabledSkillCount = slashSkills.filter((item) => item.enabled && item.available).length;
  const availableSkillCount = slashSkills.filter((item) => item.available).length;
  const readyKnowledgeCount = slashKnowledge.filter((item) => item.status === "ready").length;
  const enabledVisionCount = slashVision.filter((item) => item.enabled).length;
  const matchingRun = activeRun?.modelProfileId === profileId && activeRun.modelId === modelId ? activeRun : null;
  const statusContextWindow = matchingRun?.contextWindowTokens ?? selectedModel?.contextWindowTokens ?? defaultContextWindowTokens;
  const statusCompactLimit = matchingRun?.autoCompactTokenLimit ?? selectedModel?.autoCompactTokenLimit ?? automaticCompactLimit(statusContextWindow);
  const mcpStatus = mcpStatusLoading
    ? { text: "正在读取", kind: "loading" as const }
    : mcpStatusError
      ? { text: "状态不可用", kind: "off" as const }
      : !mcpServers
        ? { text: "读取状态", kind: "loading" as const }
        : mcpServers.length === 0
          ? { text: "未配置", kind: "off" as const }
          : activeMCPCount > 0
            ? { text: `已开启 ${activeMCPCount}/${mcpServers.length}`, kind: "on" as const }
            : transitioningMCPCount > 0
              ? { text: "连接中", kind: "loading" as const }
              : { text: `已关闭 · ${mcpServers.length} 个`, kind: "off" as const };
  const slashCommands = useMemo<SlashCommand[]>(() => [
    { id: "mcp", name: "mcp", title: "MCP Servers", description: "查看 Server、连接状态与可用工具", icon: "server", enabled: true, state: mcpStatus.text, stateKind: mcpStatus.kind },
    { id: "skill", name: "skill", title: "Skills", description: selectedProject ? `查看并选择 ${selectedProject.name} 的研究技能` : "查看已安装研究技能", icon: "skill", enabled: true },
    { id: "knowledge", name: "knowledge", title: "知识库", description: "查看当前项目文献和索引状态", icon: "library", enabled: Boolean(selectedProject), disabledReason: "请先选择科研项目" },
    { id: "compact", name: "compact", title: "压缩会话", description: "生成可校验 checkpoint 并释放上下文", icon: "refresh", enabled: Boolean(selectedConversation && messages.length && !busy && !compacting), disabledReason: busy ? "请等待当前回答完成" : compacting ? "会话正在压缩" : "当前会话还没有可压缩内容", state: compacting ? "压缩中" : activeRun?.contextCompacted ? "已有 checkpoint" : undefined, stateKind: compacting ? "loading" : activeRun?.contextCompacted ? "on" : undefined },
    { id: "model", name: "model", title: "模型", description: "选择当前会话使用的模型", icon: "model", enabled: selectableModels.length > 0, disabledReason: "还没有可用模型" },
    { id: "reasoning", name: "reasoning", title: "思考强度", description: "切换当前会话的推理档位", icon: "spark", enabled: Boolean(selectedConversation && !busy), disabledReason: busy ? "运行期间不能切换思考强度" : "请先选择研究会话", state: selectedConversation?.reasoningLevel, stateKind: "on" },
    { id: "permission", name: "permission", title: "工具权限", description: "切换 Plan 或 Full Access", icon: "shield", enabled: Boolean(selectedConversation && !busy), disabledReason: busy ? "运行期间不能切换工具权限" : "请先选择研究会话", state: selectedConversation?.permissionMode === "full_access" ? "Full Access" : selectedConversation ? "Plan" : undefined, stateKind: "on" },
    { id: "status", name: "status", title: "运行状态", description: "汇总模型、上下文和扩展能力状态", icon: "chart", enabled: Boolean(selectedConversation), disabledReason: "请先选择研究会话", state: busy ? "运行中" : "就绪", stateKind: busy ? "loading" : "on" },
    { id: "usage", name: "usage", title: "用量统计", description: "查看 Token、推理与缓存命中摘要", icon: "chart", enabled: true },
    { id: "new", name: "new", title: "新建研究会话", description: "在当前项目开始新的会话", icon: "plus", enabled: Boolean(selectedProject && !busy), disabledReason: busy ? "请先停止当前回答" : "请先选择科研项目" },
    { id: "help", name: "help", title: "命令列表", description: "查看当前可用的斜杠命令", icon: "search", enabled: true },
  ], [activeRun?.contextCompacted, busy, compacting, mcpStatus.kind, mcpStatus.text, messages.length, selectableModels.length, selectedConversation, selectedProject]);
  const filteredSlashCommands = useMemo(() => slashCommands.filter((item) => !slashQuery || item.name.includes(slashQuery) || item.title.toLowerCase().includes(slashQuery) || item.description.toLowerCase().includes(slashQuery)), [slashCommands, slashQuery]);
  const exactSlashCommand = slashCommands.find((item) => `/${item.name}` === input.trim().toLowerCase());
  const slashPanelItemCount = slashPanelLoading ? 0 : slashPanel === "mcp" ? (mcpServers?.length ?? 0) + 1 : slashPanel === "skill" ? slashSkills.length + 1 : slashPanel === "model" ? selectableModels.length + 1 : slashPanel === "reasoning" ? reasoningLevels.length : slashPanel === "permission" ? 2 : slashPanel === "mcp-detail" ? 2 : slashPanel === "knowledge" || slashPanel === "usage" ? 1 : 0;
  const slashPanelTitle = slashPanel === "mcp" ? "MCP Servers" : slashPanel === "mcp-detail" ? slashMCPServer?.name ?? "MCP Server" : slashPanel === "skill" ? "Skills" : slashPanel === "knowledge" ? "知识库" : slashPanel === "model" ? "选择模型" : slashPanel === "reasoning" ? "思考强度" : slashPanel === "permission" ? "工具权限" : slashPanel === "status" ? "运行状态" : "用量统计";
  const slashPanelMeta = slashPanelLoading ? "正在读取" : slashPanel === "mcp" ? `${mcpServers?.length ?? 0} 个 Server` : slashPanel === "skill" ? `${slashSkills.length} 个 Skill` : slashPanel === "knowledge" ? `${slashKnowledge.length} 篇文档` : slashPanel === "model" ? `${selectableModels.length} 个模型` : slashPanel === "reasoning" ? `${reasoningLevels.length} 个档位` : slashPanel === "permission" ? "2 种模式" : slashPanel === "status" ? selectedConversation?.title ?? "当前会话" : "全部模型";

  useEffect(() => {
    if (slashPanel === "reasoning") { setSlashSelected(Math.max(0, reasoningLevels.indexOf(selectedConversation?.reasoningLevel ?? "medium"))); return; }
    if (slashPanel === "permission") { setSlashSelected(selectedConversation?.permissionMode === "full_access" ? 1 : 0); return; }
    setSlashSelected(0);
  }, [selectedConversation?.permissionMode, selectedConversation?.reasoningLevel, slashPanel, slashQuery]);
  useEffect(() => { if (slashPanel === null && slashTyping) void loadMCPStatus(); }, [loadMCPStatus, slashPanel, slashTyping]);
  useEffect(() => {
    const autoDismiss = notice.startsWith("压缩成功：") || notice.startsWith("压缩部分完成：");
    if (!autoDismiss || notice.includes("状态刷新失败")) return;
    const timer = window.setTimeout(() => setNotice((current) => current === notice ? "" : current), 4_500);
    return () => window.clearTimeout(timer);
  }, [notice]);
  useEffect(() => {
    if (!slashMenuOpen) return;
    const closeOnOutsidePointer = (event: PointerEvent) => {
      const target = event.target;
      if (target instanceof Node && slashMenuRef.current?.contains(target)) return;
      setSlashPanel(null);
      setSlashMCPServer(null);
      setSlashMCPCapabilities(null);
      setSlashSelected(0);
      setInput("");
    };
    document.addEventListener("pointerdown", closeOnOutsidePointer, true);
    return () => document.removeEventListener("pointerdown", closeOnOutsidePointer, true);
  }, [slashMenuOpen]);

  async function executeSlashCommand(command: SlashCommand) {
    if (!command.enabled) {
      setNotice(command.disabledReason ?? "当前命令不可用。");
      return;
    }
    setInput(""); setSlashSelected(0);
    window.requestAnimationFrame(() => composerInputRef.current?.focus());
    switch (command.id) {
    case "mcp":
      setSlashPanel("mcp"); setSlashPanelLoading(true);
      await loadMCPStatus();
      setSlashPanelLoading(false);
      return;
    case "skill":
      setSlashPanel("skill");
      await loadSlashSkills();
      return;
    case "knowledge":
      setSlashPanel("knowledge");
      await loadSlashKnowledge();
      return;
    case "model":
      setSlashPanel("model");
      return;
    case "reasoning":
      setSlashPanel("reasoning");
      return;
    case "permission":
      setSlashPanel("permission");
      return;
    case "status":
      setSlashPanel("status");
      await loadSlashStatus();
      return;
    case "usage":
      setSlashPanel("usage");
      await loadSlashUsage();
      return;
    case "new":
      setCreateDialog({ kind: "conversation", title: "", description: "", workspacePath: "" });
      return;
    case "help":
      setInput("/");
      return;
    case "compact":
      setCompacting(true); setNotice("正在生成会话 checkpoint，请稍候…");
      try {
        const result = await backend<ContextCompactionResult>("ChatFacade", "CompactConversation", conversationId);
        const success = result.passes === 0
          ? `压缩成功：当前会话已经压缩到最新消息（checkpoint r${result.revision}）。`
          : result.complete
            ? `压缩成功：checkpoint r${result.revision}，已归纳 ${result.sourceMessageCount} 条历史消息。`
            : `压缩部分完成：已保存 checkpoint r${result.revision}；较长历史可再次执行 /compact。`;
        setNotice(success);
        try {
          const snapshot = await backend<RunSnapshot | null>("ChatFacade", "GetLatestRunSnapshot", conversationId);
          if (snapshot) applySnapshot(snapshot);
        } catch {
          setNotice(`${success} 当前状态刷新失败，重新进入会话后即可显示。`);
        }
      } catch (error) { setNotice(`压缩失败：${errorText(error)}`); }
      finally { setCompacting(false); }
    }
  }

  async function executeSlashPanelSelection(index: number) {
    if (slashPanel === "mcp") {
      const server = mcpServers?.[index];
      if (server) await openSlashMCPServer(server);
      else { setSlashPanel(null); setMcpOpen(true); }
      return;
    }
    if (slashPanel === "mcp-detail" && slashMCPServer) {
      if (index === 0) await toggleSlashMCPServer(slashMCPServer);
      else { setSlashPanel(null); setMcpOpen(true); }
      return;
    }
    if (slashPanel === "skill") {
      const skill = slashSkills[index];
      if (skill) {
        if (!skill.enabled || !skill.available) { setNotice(skill.reason || "只有当前项目已启用且可用的 Skill 才能直接选择。"); return; }
        setInput(`$${skill.id} `); setSlashPanel(null); window.requestAnimationFrame(() => composerInputRef.current?.focus());
      } else { setSlashPanel(null); setSkillsOpen(true); }
      return;
    }
    if (slashPanel === "model") {
      const selected = selectableModels[index];
      if (selected) { setProfileId(selected.profile.id); setModelId(selected.model.id); setSlashPanel(null); setInput(""); }
      else { setSlashPanel(null); setSettingsOpen(true); }
      return;
    }
    if (slashPanel === "reasoning") {
      const selected = reasoningLevels[index];
      if (selected) { await changeReasoningLevel(selected); setSlashPanel(null); }
      return;
    }
    if (slashPanel === "permission") {
      const selected: PermissionMode | undefined = (["plan", "full_access"] as PermissionMode[])[index];
      if (selected) { await changePermissionMode(selected); setSlashPanel(null); }
      return;
    }
    if (slashPanel === "knowledge") { setSlashPanel(null); setKnowledgeOpen(true); return; }
    if (slashPanel === "usage") { setSlashPanel(null); setUsageOpen(true); }
  }

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

  const resolveApproval = useCallback(async (approval: Approval, allow: boolean) => {
    if (resolvingApprovalId) return;
    setResolvingApprovalId(approval.id);
    try {
      await backend("PermissionFacade", "ResolveApproval", { approvalId: approval.id, allow, scope: "call" });
      applySnapshot(await backend<RunSnapshot>("ChatFacade", "GetRunSnapshot", approval.runId));
    } catch (error) { setNotice(errorText(error)); }
    finally { setResolvingApprovalId(""); }
  }, [applySnapshot, resolvingApprovalId]);

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
      <section className="chat" aria-live="polite" ref={chatRef} onScroll={(event) => {
        const chat = event.currentTarget;
        autoFollowRef.current = chat.scrollHeight - chat.scrollTop - chat.clientHeight < 96;
      }}>
        {messages.length === 0
          ? <EmptyState hasProject={Boolean(projectId)} hasConversation={Boolean(conversationId)} hasProfile={Boolean(profileId && modelId)} openSettings={() => setSettingsOpen(true)} createConversation={() => setCreateDialog({ kind: "conversation", title: "", description: "", workspacePath: "" })} setPrompt={setInput}/>
          : <div className="message-stack">{messages.map((message) => {
            const messageRun = message.role === "assistant" && activeRun && activeRun.id === message.runId ? activeRun : undefined;
            return <MessageRow
              key={message.id}
              message={message}
              providerName={selectedProfile?.name ?? "SciAide"}
              run={messageRun}
              runActive={Boolean(messageRun && busy)}
              retryStatus={messageRun ? retryStatus : null}
              runSteps={messageRun ? runSteps : emptyRunSteps}
              toolCalls={messageRun ? toolCalls : emptyToolCalls}
              approvals={messageRun ? pendingApprovals : emptyApprovals}
              revealing={revealingMessageIds.has(message.id)}
              resolvingApprovalId={messageRun ? resolvingApprovalId : ""}
              resolveApproval={resolveApproval}
            />;
          })}</div>}
      </section>
      <footer className="composer-wrap">
        {notice && <div className="notice"><Icon name="shield" size={15}/><span>{notice}</span><button onClick={() => setNotice("")}><Icon name="close" size={14}/></button></div>}
        {activeRun?.errorMessage && <RunErrorNotice run={activeRun}/>}
        {slashMenuOpen && <div ref={slashMenuRef} className="slash-command-menu" role="listbox" aria-label="斜杠命令">
          {slashPanel === null && <><header><b>命令</b><span>{filteredSlashCommands.length} 项</span></header><div>{filteredSlashCommands.length ? filteredSlashCommands.map((command, index) => <button type="button" role="option" aria-selected={index === slashSelected} className={index === slashSelected ? "selected" : ""} key={command.id} disabled={!command.enabled} onMouseDown={(event) => { event.preventDefault(); void executeSlashCommand(command); }} onMouseEnter={() => setSlashSelected(index)}><span className="slash-command-icon"><Icon name={command.icon} size={16}/></span><span className="slash-command-copy"><b>/{command.name} <i>{command.title}</i></b><small>{command.enabled ? command.description : command.disabledReason}</small></span>{command.state && <span className={`slash-command-state ${command.stateKind ?? "off"}`}><i/>{command.state}</span>}</button>) : <p>没有匹配的本地命令</p>}</div></>}
          {slashPanel !== null && <header className="slash-panel-header"><button type="button" aria-label="返回命令列表" onMouseDown={(event) => { event.preventDefault(); if (slashPanel === "mcp-detail") { setSlashPanel("mcp"); setSlashMCPServer(null); setSlashMCPCapabilities(null); } else { setSlashPanel(null); setInput("/"); } }}><Icon name="back" size={15}/></button><b>{slashPanelTitle}</b><span>{slashPanelMeta}</span></header>}
          {slashPanel === "mcp" && <div className="slash-runtime-list">{slashPanelLoading ? <p>正在读取 MCP 运行状态…</p> : mcpStatusError ? <p>无法读取 MCP 状态</p> : mcpServers?.length ? mcpServers.map((server, index) => { const state = mcpRuntimeState(server); return <button type="button" className={slashSelected === index ? "selected" : ""} key={server.id} onMouseEnter={() => setSlashSelected(index)} onMouseDown={(event) => { event.preventDefault(); void openSlashMCPServer(server); }}><span className="slash-command-icon"><Icon name="server" size={16}/></span><span className="slash-command-copy"><b>{server.name} <i>{server.namespace}</i></b><small>{server.toolCount} tools · {server.resourceCount} resources · {server.promptCount} prompts{server.lastError ? ` · ${server.lastError}` : ""}</small></span><span className={`slash-command-state ${state.kind}`}><i/>{state.label}</span></button>; }) : <p>还没有配置 MCP Server</p>}<button type="button" className={`slash-manage ${slashSelected === (mcpServers?.length ?? 0) ? "selected" : ""}`} onMouseEnter={() => setSlashSelected(mcpServers?.length ?? 0)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setMcpOpen(true); }}><Icon name="settings" size={14}/> 管理 MCP 配置</button></div>}
          {slashPanel === "mcp-detail" && slashMCPServer && <div className="slash-runtime-detail"><div className="slash-runtime-summary"><span className={`slash-command-state ${mcpRuntimeState(slashMCPServer).kind}`}><i/>{mcpRuntimeState(slashMCPServer).label}</span><b>{slashMCPServer.toolCount} tools · {slashMCPServer.resourceCount} resources · {slashMCPServer.promptCount} prompts</b></div>{slashPanelLoading ? <p>正在读取 Server 能力…</p> : slashMCPCapabilities ? <><section><b>Tools</b>{slashMCPCapabilities.tools.length ? <div className="slash-tool-list">{slashMCPCapabilities.tools.map((tool) => <span key={tool.qualifiedName} title={tool.description}><code>{tool.originalName}</code><small>{tool.description || tool.qualifiedName}</small></span>)}</div> : <p>此 Server 没有暴露工具</p>}</section>{slashMCPCapabilities.resources.length > 0 && <section><b>Resources</b><p>{slashMCPCapabilities.resources.join(" · ")}</p></section>}{slashMCPCapabilities.prompts.length > 0 && <section><b>Prompts</b><p>{slashMCPCapabilities.prompts.join(" · ")}</p></section>}</> : <p>{slashMCPServer.lastError || "Server 关闭时不加载能力列表。"}</p>}<div className="slash-detail-actions"><button type="button" className={slashSelected === 0 ? "selected" : ""} disabled={slashMCPAction || slashMCPServer.status === "starting" || slashMCPServer.status === "initializing" || slashMCPServer.status === "stopping" || (!(slashMCPServer.status === "ready" || slashMCPServer.status === "degraded") && (!slashMCPServer.enabled || slashMCPServer.trust !== "user_trusted"))} onMouseEnter={() => setSlashSelected(0)} onMouseDown={(event) => { event.preventDefault(); void toggleSlashMCPServer(slashMCPServer); }}><Icon name={slashMCPAction ? "refresh" : slashMCPServer.status === "ready" || slashMCPServer.status === "degraded" ? "stop" : "server"} size={14}/>{slashMCPAction ? "处理中" : slashMCPServer.status === "ready" || slashMCPServer.status === "degraded" ? "关闭 Server" : "启动 Server"}</button><button type="button" className={slashSelected === 1 ? "selected" : ""} onMouseEnter={() => setSlashSelected(1)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setMcpOpen(true); }}><Icon name="settings" size={14}/> 配置</button></div></div>}
          {slashPanel === "skill" && <div className="slash-runtime-list">{slashPanelLoading ? <p>正在读取项目 Skills…</p> : slashSkills.length ? slashSkills.map((skill, index) => <button type="button" className={slashSelected === index ? "selected" : ""} key={skill.id} aria-disabled={!skill.enabled || !skill.available} onMouseEnter={() => setSlashSelected(index)} onMouseDown={(event) => { event.preventDefault(); void executeSlashPanelSelection(index); }}><span className="slash-command-icon"><Icon name="skill" size={16}/></span><span className="slash-command-copy"><b>{skill.name} <i>{skill.id}@{skill.version}</i></b><small>{skill.available ? skill.description : skill.reason || "Skill 当前不可用"}</small></span><span className={`slash-command-state ${skill.enabled && skill.available ? "on" : "off"}`}><i/>{skill.enabled ? skill.available ? "已启用" : "不可用" : "未启用"}</span></button>) : <p>当前没有已安装 Skill</p>}<button type="button" className={`slash-manage ${slashSelected === slashSkills.length ? "selected" : ""}`} onMouseEnter={() => setSlashSelected(slashSkills.length)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setSkillsOpen(true); }}><Icon name="settings" size={14}/> 管理 Skills</button></div>}
          {slashPanel === "knowledge" && <div className="slash-runtime-list">{slashPanelLoading ? <p>正在读取项目知识库…</p> : slashKnowledge.length ? slashKnowledge.map((document) => <div className="slash-runtime-row" key={document.id}><span className={`slash-command-icon quality-${document.diagnostic.quality}`}><Icon name="library" size={16}/></span><span className="slash-command-copy"><b>{document.title}</b><small>{document.diagnostic.summary} · {document.chunkCount} chunks</small></span><span className={`slash-command-state ${document.status === "ready" ? "on" : document.status === "indexing" ? "loading" : "off"}`}><i/>{knowledgeDisplayStatus(document)}</span></div>) : <p>当前项目知识库为空</p>}<button type="button" className={`slash-manage ${slashSelected === 0 ? "selected" : ""}`} onMouseEnter={() => setSlashSelected(0)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setKnowledgeOpen(true); }}><Icon name="settings" size={14}/> 管理知识库</button></div>}
          {slashPanel === "model" && <div className="slash-runtime-list">{selectableModels.map(({ profile, model }, index) => { const selected = profile.id === profileId && model.id === modelId; return <button type="button" className={slashSelected === index ? "selected" : ""} key={modelKey(profile.id, model.id)} onMouseEnter={() => setSlashSelected(index)} onMouseDown={(event) => { event.preventDefault(); void executeSlashPanelSelection(index); }}><span className="slash-command-icon"><Icon name="model" size={16}/></span><span className="slash-command-copy"><b>{model.id} <i>{profile.name}</i></b><small>{modelContextSummary(model)} · {modelReasoningSummary(model).label}</small></span>{selected && <span className="slash-command-state on"><i/>当前</span>}</button>; })}<button type="button" className={`slash-manage ${slashSelected === selectableModels.length ? "selected" : ""}`} onMouseEnter={() => setSlashSelected(selectableModels.length)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setSettingsOpen(true); }}><Icon name="settings" size={14}/> 管理模型与 API</button></div>}
          {slashPanel === "reasoning" && <div className="slash-runtime-list">{reasoningLevels.map((level, index) => { const current = selectedConversation?.reasoningLevel === level; const display = reasoningDisplay(level, selectedModel, current ? activeRun : null, profileId, modelId); return <button type="button" className={slashSelected === index ? "selected" : ""} key={level} onMouseEnter={() => setSlashSelected(index)} onMouseDown={(event) => { event.preventDefault(); void executeSlashPanelSelection(index); }}><span className="slash-command-icon"><Icon name="spark" size={16}/></span><span className="slash-command-copy"><b>{level} <i>{reasoningDescriptions[level]}</i></b><small>{display.text}</small></span>{current && <span className="slash-command-state on"><i/>当前</span>}</button>; })}</div>}
          {slashPanel === "permission" && <div className="slash-runtime-list">{([{ mode: "plan" as PermissionMode, title: "Plan", description: "Workspace 内只读免确认；越界读取、写入和其他工具需确认" }, { mode: "full_access" as PermissionMode, title: "Full Access", description: "边界校验通过后，已注册工具可自动执行" }]).map((item, index) => { const current = selectedConversation?.permissionMode === item.mode; return <button type="button" className={slashSelected === index ? "selected" : ""} key={item.mode} onMouseEnter={() => setSlashSelected(index)} onMouseDown={(event) => { event.preventDefault(); void executeSlashPanelSelection(index); }}><span className="slash-command-icon"><Icon name="shield" size={16}/></span><span className="slash-command-copy"><b>{item.title}</b><small>{item.description}</small></span>{current && <span className="slash-command-state on"><i/>当前</span>}</button>; })}</div>}
          {slashPanel === "status" && <div className="slash-runtime-detail slash-status-detail">{slashPanelLoading ? <p>正在汇总当前会话状态…</p> : <>{slashStatusErrors.length > 0 && <p className="slash-status-warning">部分状态不可用：{slashStatusErrors.join("、")}</p>}<div className="slash-status-list"><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="model" size={16}/></span><span className="slash-command-copy"><b>{selectedModel?.id ?? "未选择模型"} <i>{selectedProfile?.name}</i></b><small>{selectedProfile ? protocolLabels[selectedProfile.apiProtocol] : "模型配置不可用"}</small></span><span className={`slash-command-state ${selectedProfile?.secretConfigured ? "on" : "off"}`}><i/>{selectedProfile?.secretConfigured ? "API 已配置" : "缺少 Key"}</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="refresh" size={16}/></span><span className="slash-command-copy"><b>上下文窗口 <i>{statusContextWindow.toLocaleString()} tokens</i></b><small>自动压缩阈值 {statusCompactLimit.toLocaleString()}{matchingRun ? ` · 最近输入 ${matchingRun.inputTokens.toLocaleString()}` : ""}</small></span><span className={`slash-command-state ${matchingRun?.contextCompacted ? "on" : "off"}`}><i/>{matchingRun?.contextCompacted ? "已有 checkpoint" : "未压缩"}</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="spark" size={16}/></span><span className="slash-command-copy"><b>思考强度 <i>{selectedConversation?.reasoningLevel}</i></b><small>{reasoning.text}</small></span><span className="slash-command-state on"><i/>已设置</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="shield" size={16}/></span><span className="slash-command-copy"><b>工具权限 <i>{selectedConversation?.permissionMode === "full_access" ? "Full Access" : "Plan"}</i></b><small>{selectedConversation?.permissionMode === "full_access" ? "已注册工具通过边界校验后自动执行" : "越界读取、写入和其他工具需要确认"}</small></span><span className="slash-command-state on"><i/>当前</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="server" size={16}/></span><span className="slash-command-copy"><b>MCP Servers <i>{mcpServers?.length ?? 0} 个</i></b><small>{activeMCPCount > 0 ? `${activeMCPCount} 个 Server 已连接并注册工具` : "当前没有已连接 Server"}</small></span><span className={`slash-command-state ${mcpStatus.kind}`}><i/>{mcpStatus.text}</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="skill" size={16}/></span><span className="slash-command-copy"><b>Skills <i>{enabledSkillCount}/{availableSkillCount}</i></b><small>{availableSkillCount > 0 ? "当前项目已启用 / 可用" : "当前没有可用 Skill"}</small></span><span className={`slash-command-state ${enabledSkillCount > 0 ? "on" : "off"}`}><i/>{enabledSkillCount > 0 ? "已启用" : "未启用"}</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="library" size={16}/></span><span className="slash-command-copy"><b>知识库 <i>{readyKnowledgeCount}/{slashKnowledge.length}</i></b><small>{slashKnowledge.length > 0 ? "已就绪 / 全部项目文献" : "当前项目知识库为空"}</small></span><span className={`slash-command-state ${readyKnowledgeCount > 0 ? "on" : slashKnowledge.some((item) => item.status === "indexing") ? "loading" : "off"}`}><i/>{readyKnowledgeCount > 0 ? "可检索" : slashKnowledge.some((item) => item.status === "indexing") ? "索引中" : "无文献"}</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="search" size={16}/></span><span className="slash-command-copy"><b>识图兜底 <i>{enabledVisionCount}/{slashVision.length}</i></b><small>{slashVision.length > 0 ? slashVision.map((item) => item.modelId).join(" → ") : "尚未配置自定义视觉渠道"}</small></span><span className={`slash-command-state ${enabledVisionCount > 0 ? "on" : "off"}`}><i/>{enabledVisionCount > 0 ? "可用" : slashVision.length > 0 ? "未启用" : "未配置"}</span></div></div></>}</div>}
          {slashPanel === "usage" && <div className="slash-runtime-detail">{slashPanelLoading ? <p>正在读取用量统计…</p> : slashUsage ? <><div className="slash-usage-grid"><span><small>实际总 Token</small><b>{slashUsage.summary.realTotalTokens.toLocaleString()}</b></span><span><small>模型请求</small><b>{slashUsage.summary.requestCount.toLocaleString()}</b></span><span><small>推理 Token</small><b>{slashUsage.summary.reasoningTokens.toLocaleString()}</b></span><span><small>缓存命中率</small><b>{slashUsage.summary.cacheDataAvailable ? `${(slashUsage.summary.cacheHitRate * 100).toFixed(1)}%` : "无数据"}</b></span></div><section><b>按模型</b>{slashUsage.models.slice(0, 6).map((item) => <div className="slash-usage-model" key={`${item.modelProfileId}:${item.modelId}`}><span>{item.profileName} · {item.modelId}</span><b>{item.realTotalTokens.toLocaleString()}</b></div>)}</section></> : <p>暂无用量数据</p>}<div className="slash-detail-actions"><button type="button" className={slashSelected === 0 ? "selected" : ""} onMouseEnter={() => setSlashSelected(0)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setUsageOpen(true); }}><Icon name="chart" size={14}/> 打开完整统计</button></div></div>}
        </div>}
        <form className="composer" onSubmit={(event) => void send(event)}>
          {pendingAttachments.length > 0 && <div className="pending-attachments">{pendingAttachments.map((item) => <div key={item.id}><span><Icon name={item.format === "image" ? "model" : "skill"} size={15}/></span><b title={item.originalName}>{item.originalName}</b><small>{attachmentSummary(item)}</small><button type="button" aria-label={`移除 ${item.originalName}`} title="移除附件" onClick={() => setPendingAttachments((current) => current.filter((value) => value.id !== item.id))}><Icon name="close" size={13}/></button></div>)}</div>}
          {pendingAttachments.some((item) => item.format === "image") && <div className="image-routing-note"><Icon name="model" size={14}/><span>图片优先由当前模型识别；API 明确拒绝图片输入后，SciAide 将按顺序调用已配置的自定义识图渠道，并在处理记录中标明实际模型。</span></div>}
          <textarea ref={composerInputRef} value={input} onChange={(event) => { setInput(event.target.value); if (slashPanel !== null) setSlashPanel(null); }} onKeyDown={(event) => {
            if (slashPanel !== null) {
              if (event.key === "Escape") { event.preventDefault(); if (slashPanel === "mcp-detail") { setSlashPanel("mcp"); setSlashMCPServer(null); setSlashMCPCapabilities(null); } else { setSlashPanel(null); setInput("/"); } }
              else if (slashPanelItemCount > 0 && event.key === "ArrowDown") { event.preventDefault(); setSlashSelected((current) => (current + 1) % slashPanelItemCount); }
              else if (slashPanelItemCount > 0 && event.key === "ArrowUp") { event.preventDefault(); setSlashSelected((current) => (current - 1 + slashPanelItemCount) % slashPanelItemCount); }
              else if (slashPanelItemCount > 0 && ((event.key === "Enter" && !event.shiftKey) || event.key === "Tab")) { event.preventDefault(); void executeSlashPanelSelection(Math.min(slashSelected, slashPanelItemCount - 1)); }
              else if (event.key === "Enter" && !event.shiftKey) event.preventDefault();
              return;
            }
            if (slashTyping && filteredSlashCommands.length) {
              if (event.key === "ArrowDown") { event.preventDefault(); setSlashSelected((current) => (current + 1) % filteredSlashCommands.length); return; }
              if (event.key === "ArrowUp") { event.preventDefault(); setSlashSelected((current) => (current - 1 + filteredSlashCommands.length) % filteredSlashCommands.length); return; }
              if ((event.key === "Enter" && !event.shiftKey) || event.key === "Tab") { event.preventDefault(); const selected = filteredSlashCommands[Math.min(slashSelected, filteredSlashCommands.length - 1)]; if (selected) void executeSlashCommand(selected); return; }
              if (event.key === "Escape") { event.preventDefault(); setInput(""); return; }
            }
            if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); }
          }} placeholder={!conversationId ? "输入 / 打开功能，或先创建研究会话" : busy ? "输入新指令可中断当前生成并继续…" : "向 SciAide 描述研究问题，或输入 / 使用命令…"}/>
          <div className="composer-actions"><span>{busy ? "发送新消息将中断当前生成并立即继续" : usage || <><kbd>Enter</kbd> 发送 · <kbd>Shift Enter</kbd> 换行</>}</span><div className="composer-buttons"><button type="button" className="attach" aria-label="添加当前对话附件" title="仅添加到当前对话，不会加入项目知识库" disabled={!conversationId || !projectId || importingAttachments} onClick={() => void attachDocuments()}><Icon name={importingAttachments ? "refresh" : "paperclip"} size={17}/></button>{busy && <button type="button" className="stop" onClick={() => activeRun && void backend<void>("ChatFacade", "CancelRun", activeRun.id).catch((error: unknown) => setNotice(errorText(error)))}><Icon name="stop" size={15}/> 停止</button>}<button className="send" aria-label={busy ? "中断并发送" : "发送"} disabled={!exactSlashCommand && ((!input.trim() && pendingAttachments.length === 0) || !conversationId || !profileId || !modelId)}><Icon name="send" size={17}/></button></div></div>
        </form>
        <p className="composer-hint">AI 可能会出错，重要科研结论请核验原始来源。</p>
      </footer>
    </main>
    {settingsOpen && <ModelSettings profiles={profiles} close={() => setSettingsOpen(false)} refresh={loadProfiles} select={setProfileId}/>}
    {mcpOpen && <MCPSettings close={() => { setMcpOpen(false); void loadMCPStatus(); }}/>}
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

  async function changePermissionMode(mode: PermissionMode) {
    if (!selectedConversation || busy || selectedConversation.permissionMode === mode) return;
    try {
      const updated = await backend<Conversation>("ConversationFacade", "SetPermissionMode", selectedConversation.id, mode);
      setConversations((current) => current.map((item) => item.id === updated.id ? updated : item));
      setNotice(mode === "full_access" ? "已启用 Full Access：注册工具通过边界校验后将自动执行。" : "已切换到 Plan：Workspace 内只读免确认，越界读取、写入和其他工具需要确认。");
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

const MessageRow = memo(function MessageRow({ message, providerName, run, runActive, retryStatus, runSteps, toolCalls, approvals, revealing, resolvingApprovalId, resolveApproval }: {
  message: Message;
  providerName: string;
  run?: Run;
  runActive: boolean;
  retryStatus: RetryStatus | null;
  runSteps: RunStep[];
  toolCalls: ToolCall[];
  approvals: Approval[];
  revealing: boolean;
  resolvingApprovalId: string;
  resolveApproval: (approval: Approval, allow: boolean) => Promise<void>;
}) {
  const attachments = attachmentsOf(message);
  return <article className={`message ${message.role}${revealing ? " revealing" : ""}`} aria-busy={revealing}>
    <div className="avatar">{message.role === "user" ? "你" : <Icon name="spark" size={17}/>}</div>
    <div className="message-body">
      <div className="message-meta"><b>{message.role === "user" ? "你" : providerName}</b>{message.status === "incomplete" && <span>生成已中断</span>}</div>
      {attachments.length > 0 && <div className="message-attachments">{attachments.map((item) => <div className="attachment-card" key={item.attachmentId}><span><Icon name={item.format === "image" ? "model" : "skill"} size={16}/></span><div><b title={item.originalName}>{item.originalName}</b><small>{attachmentSummary(item)}</small></div></div>)}</div>}
      {message.role === "assistant" && run && !runActive && <RunProcess run={run} active={false} retryStatus={null} steps={runSteps} reasoning={message.reasoning} toolCalls={toolCalls} approvals={approvals} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>}
      {message.role === "assistant" && !run && message.runId && <HistoricalRunProcess runId={message.runId} reasoning={message.reasoning} resolveApproval={resolveApproval}/>}
      <CitedAnswer message={message} revealing={revealing}/>
      {message.role === "assistant" && run && runActive && <RunProcess run={run} active retryStatus={retryStatus} steps={runSteps} reasoning={message.reasoning} toolCalls={toolCalls} approvals={approvals} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>}
    </div>
  </article>;
});

function CitedAnswer({ message, revealing = false }: { message: Message; revealing?: boolean }) {
  const [selectedReference, setSelectedReference] = useState("");
  const [showRawQuote, setShowRawQuote] = useState(false);
  const [copied, setCopied] = useState(false);
  useEffect(() => setSelectedReference(""), [message.id]);
  useEffect(() => setShowRawQuote(false), [message.id, selectedReference]);
  useEffect(() => setCopied(false), [message.id]);
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
  async function copyAnswer() {
    await copyToClipboard(text);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  }
  return <>
    <div className="bubble">{text ? content : ""}</div>
    {message.role === "assistant" && text && !revealing && <div className="message-actions"><button type="button" aria-label="复制回答" title={copied ? "已复制" : "复制回答"} onClick={() => void copyAnswer()}><Icon name={copied ? "check" : "copy"} size={14}/><span>{copied ? "已复制" : "复制"}</span></button></div>}
    {selected && <section className="citation-detail" aria-label="引用证据">
      <header><span><Icon name="library" size={14}/></span><div><b>{selected.sourceName}</b><small>{selected.locator}{selected.title ? ` · ${selected.title}` : ""}</small></div><button type="button" className="citation-view-toggle" title={showRawQuote ? "恢复整理后的文本" : "查看参与证据校验的原始文本"} onClick={() => setShowRawQuote((value) => !value)}>{showRawQuote ? "整理文本" : "原始文本"}</button><button type="button" aria-label="关闭引用详情" onClick={() => setSelectedReference("")}><Icon name="close" size={13}/></button></header>
      <blockquote>{showRawQuote ? selected.quote : formatCitationQuote(selected.quote)}</blockquote>
      <footer><span>已验证引用</span><code>{selected.reference}</code><small title={selected.quoteSha256}>证据 {selected.quoteSha256.slice(0, 12)}</small></footer>
    </section>}
  </>;
}

function runDuration(run: Run, now: number) {
  const start = Date.parse(run.startedAt ?? run.createdAt);
  const end = run.completedAt ? Date.parse(run.completedAt) : now;
  const seconds = Math.max(0, Math.round((end - start) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${seconds % 60}s`;
}

function RunProcess({ run, active, retryStatus, steps, reasoning, toolCalls, approvals, resolvingApprovalId, resolveApproval, initiallyOpen = false }: {
  run: Run; active: boolean; steps: RunStep[]; reasoning?: MessageReasoning; toolCalls: ToolCall[]; approvals: Approval[];
  retryStatus: RetryStatus | null;
  resolvingApprovalId: string; resolveApproval: (approval: Approval, allow: boolean) => Promise<void>; initiallyOpen?: boolean;
}) {
  const [open, setOpen] = useState(active || initiallyOpen);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => setOpen(active || initiallyOpen), [active, initiallyOpen, run.id]);
  useEffect(() => {
    if (!active) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  const waiting = run.status === "waiting_approval" || approvals.length > 0;
  const failed = ["failed", "cancelled", "interrupted"].includes(run.status);
  const title = waiting ? "等待确认" : active ? "处理中" : failed ? "处理已停止" : "已处理";
  const newestCall = toolCalls.at(-1);
  const liveDetail = retryStatus?.message ?? (waiting ? "需要确认工具调用后继续" : newestCall && ["pending", "running"].includes(newestCall.status) ? `正在使用 ${newestCall.toolName}` : run.outputTokens > 0 ? "正在生成最终回答" : "正在分析问题");
  const toggle = <button type="button" className="run-process-toggle" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
      <span className={`run-process-state ${active && !waiting ? "spinning" : ""}`}>{active && !waiting ? <span/> : <Icon name={failed ? "close" : waiting ? "shield" : "check"} size={13}/>}</span>
      <b>{title} {runDuration(run, now)}</b>
      {active && <small>{liveDetail}</small>}
      <i>{open ? "收起" : "展开"}</i>
    </button>;
  const detail = open && <div className="run-process-detail">
      <ReasoningPrelude run={run} reasoning={reasoning}/>
      <RunTimeline run={run} steps={steps} toolCalls={toolCalls} approvals={approvals} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>
      {!steps.length && !toolCalls.length && !run.reasoningObserved && !run.reasoningSummary && <p className="run-process-empty">本轮未产生工具调用或可展示的推理摘要。</p>}
    </div>;
  return <section className={`run-process ${active ? "active" : "complete"} ${waiting ? "waiting" : ""}`}>
    {!active && toggle}
    {detail}
    {active && toggle}
  </section>;
}

function HistoricalRunProcess({ runId, reasoning, resolveApproval }: {
  runId: string;
  reasoning?: MessageReasoning;
  resolveApproval: (approval: Approval, allow: boolean) => Promise<void>;
}) {
  const [snapshot, setSnapshot] = useState<RunSnapshot | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    if (loading) return;
    setLoading(true);
    setError("");
    try {
      setSnapshot(await backend<RunSnapshot>("ChatFacade", "GetRunSnapshot", runId));
    } catch (value) {
      setError(errorText(value));
    } finally {
      setLoading(false);
    }
  }

  if (snapshot) return <RunProcess run={snapshot.run} active={false} retryStatus={null} steps={snapshot.runSteps ?? []} reasoning={reasoning} toolCalls={snapshot.toolCalls ?? []} approvals={snapshot.pendingApprovals ?? []} resolvingApprovalId="" resolveApproval={resolveApproval} initiallyOpen/>;

  const stopped = ["failed", "cancelled", "interrupted"].includes(reasoning?.status ?? "");
  return <section className="run-process complete">
    <button type="button" className="run-process-toggle" aria-expanded={false} onClick={() => void load()} disabled={loading} title={error || "读取该历史 Run 的处理记录"}>
      <span className="run-process-state"><Icon name={stopped ? "close" : "check"} size={13}/></span>
      <b>{stopped ? "处理已停止" : "已处理"}</b>
      <i>{loading ? "读取中" : error ? "重试" : "展开"}</i>
    </button>
  </section>;
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

function ReasoningPrelude({ run, reasoning }: { run?: Run; reasoning?: MessageReasoning }) {
  if (!run && !reasoning) return null;
  const status = run?.status ?? reasoning?.status ?? "completed";
  const requestedLevel = run?.requestedReasoningLevel ?? reasoning?.requestedLevel ?? "medium";
  const resolvedLevel = run?.resolvedReasoningLevel ?? reasoning?.resolvedLevel;
  const reasoningTokens = run?.reasoningTokens ?? reasoning?.tokens ?? 0;
  const signatureObserved = run?.reasoningSignatureObserved ?? reasoning?.signatureObserved ?? false;
  const active = !["completed", "failed", "cancelled", "interrupted"].includes(status);
  const observed = (run?.reasoningObserved ?? reasoning?.observed ?? false) || reasoningTokens > 0;
  const level = resolvedLevel || requestedLevel;
  const levelText = resolvedLevel && resolvedLevel !== requestedLevel ? `${requestedLevel} → ${resolvedLevel}` : level;
  const providerSummary = (run?.reasoningSummary ?? reasoning?.summary)?.trim();
  let detail = `已请求 ${levelText} 思考强度，供应商未返回可展示的推理摘要。`;
  if (active) detail = `正在以 ${levelText} 强度分析问题，完成后将给出回答。`;
  if (observed) detail = `模型已使用 ${levelText} 强度完成内部分析，供应商返回了可核验的推理状态。`;
  if (providerSummary) detail = providerSummary;
  const title = active && !observed && !providerSummary ? "正在思考" : "推理摘要";
  return <section className={`reasoning-prelude ${active ? "active" : observed ? "observed" : "unverified"}`} aria-label={title}>
    <span className="reasoning-prelude-icon"><Icon name="spark" size={14}/></span>
    <div><header><b>{title}</b><small>{levelText}</small></header><p>{detail}</p><footer>{reasoningTokens > 0 && <span>{reasoningTokens.toLocaleString()} 推理 Token</span>}{providerSummary && <span>供应商摘要</span>}{signatureObserved && <span>签名已验证</span>}</footer></div>
  </section>;
}

function RunTimeline({ run, steps, toolCalls, approvals, resolvingApprovalId, resolveApproval }: {
  run: Run; steps: RunStep[]; toolCalls: ToolCall[]; approvals: Approval[]; resolvingApprovalId: string;
  resolveApproval: (approval: Approval, allow: boolean) => Promise<void>;
}) {
  const timeline = [
    ...steps.map((step) => ({ kind: "step" as const, at: Date.parse(step.completedAt), step })),
    ...toolCalls.map((call) => ({ kind: "tool" as const, at: Date.parse(call.createdAt), call })),
  ].sort((left, right) => left.at - right.at || (left.kind === "step" ? -1 : 1));
  if (!timeline.length) return null;
  return <section className="run-timeline" aria-label="处理时间线">{timeline.map((item) => item.kind === "step"
    ? <article className={`run-step ${item.step.commentary?.startsWith("[识图兜底]") ? "multimodal-fallback" : ""}`} key={`step-${item.step.turnIndex}`}><header><span>{item.step.turnIndex}</span><b>{item.step.commentary?.startsWith("[识图兜底]") ? "识图兜底" : "模型处理"}</b></header>{item.step.reasoningSummary && item.step.reasoningSummary !== run.reasoningSummary && <p>{item.step.reasoningSummary}</p>}{item.step.commentary && <p>{item.step.commentary}</p>}</article>
    : <ToolActivityCard key={item.call.id} call={item.call} approval={approvals.find((approval) => approval.toolCallId === item.call.id)} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>)}
  </section>;
}

function ToolActivityCard({ call, approval, resolvingApprovalId, resolveApproval }: { call: ToolCall; approval?: Approval; resolvingApprovalId: string; resolveApproval: (approval: Approval, allow: boolean) => Promise<void> }) {
  const argumentText = JSON.stringify(call.arguments ?? {}, null, 2);
  return <article className={`tool-card ${call.status}`}>
    <header><span className="tool-icon"><Icon name="tool" size={15}/></span><div><b>{call.toolName}</b><small>v{call.toolVersion} · {toolStatusText[call.status] ?? call.status}</small></div><span className={`risk ${call.risk}`}>{call.risk}</span></header>
    <details><summary>查看参数与资源</summary><pre>{argumentText}</pre>{call.permissions.length > 0 && <div className="permission-list">{call.permissions.map((permission) => <span key={`${permission.kind}:${permission.resource}`}><b>{permission.kind}</b>{permission.resource || "全部资源"}</span>)}</div>}</details>
    {approval && <div className="approval-panel"><div><b>需要你的确认</b><p>Plan 模式下，本次工具调用只有在接受后才会执行。风险标签仅供参考，决定权完全属于你。</p></div><div className="approval-actions"><button disabled={Boolean(resolvingApprovalId)} onClick={() => void resolveApproval(approval, false)}>拒绝</button><button className="accept" disabled={Boolean(resolvingApprovalId)} onClick={() => void resolveApproval(approval, true)}>{resolvingApprovalId === approval.id ? "处理中…" : "Accept"}</button></div></div>}
    {call.result && <div className={`tool-result ${call.result.status}`}><span>{call.result.text || toolStatusText[call.status] || call.status}</span>{call.result.truncated && <small>结果已截断</small>}{call.result.meta?.durationMillis !== undefined && <small>{call.result.meta.durationMillis} ms</small>}</div>}
    {!call.result && call.errorMessage && <div className="tool-result error">{call.errorMessage}</div>}
  </article>;
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
const knowledgeStageText: Record<KnowledgeJob["stage"], string> = { queued: "等待索引", loading: "读取文档", chunking: "分块/向量化", indexing: "提交索引", completed: "可检索", failed: "索引失败", cancelled: "已取消" };
const qualityText: Record<ParseDiagnostic["quality"], string> = { good: "解析正常", warning: "需要核对", poor: "文本不足", unavailable: "解析失败" };
const documentKind = (name: string) => name.includes(".") ? name.split(".").pop()?.toUpperCase() ?? "FILE" : "FILE";
const compactDate = (value: string) => new Date(value).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
const knowledgeJobActive = (value: KnowledgeDocument) => value.job?.status === "queued" || value.job?.status === "running";
const knowledgeDisplayStatus = (value: KnowledgeDocument) => value.job ? knowledgeStageText[value.job.stage] : knowledgeStatusText[value.status];

function KnowledgeLibrary({ project, close }: { project: Project; close: () => void }) {
  const [documents, setDocuments] = useState<KnowledgeDocument[]>([]);
  const [loading, setLoading] = useState(true);
  const [importing, setImporting] = useState(false);
  const [removing, setRemoving] = useState("");
  const [taskAction, setTaskAction] = useState("");
  const [diagnosticOpen, setDiagnosticOpen] = useState("");
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
    if (!documents.some(knowledgeJobActive)) return;
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
    if (removing || knowledgeJobActive(value) || !window.confirm(`将“${value.title}”移出项目知识库？\n\n索引会删除，但原附件和历史聊天记录仍会保留。`)) return;
    setRemoving(value.id); setFeedback("");
    try {
      await backend("KnowledgeFacade", "RemoveDocument", project.id, value.id);
      setDocuments((current) => current.filter((item) => item.id !== value.id));
      setFeedback("已移出知识库，原附件仍保留在项目中。");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setRemoving(""); }
  }

  async function controlDocument(action: "cancel" | "retry" | "rebuild", value: KnowledgeDocument) {
    if (taskAction) return;
    if (action === "rebuild" && !window.confirm(`重新建立“${value.title}”的索引？\n\n当前可用内容会保留到新任务提交完成。`)) return;
    setTaskAction(`${action}:${value.id}`); setFeedback("");
    const method = action === "cancel" ? "CancelDocument" : action === "retry" ? "RetryDocument" : "RebuildDocument";
    try {
      await backend<KnowledgeJob>("KnowledgeFacade", method, project.id, value.id);
      await load(true);
      setFeedback(action === "cancel" ? "已请求取消索引任务。" : action === "retry" ? "失败任务已重新加入队列。" : "文档已提交重建，旧索引会保留到提交完成。");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setTaskAction(""); }
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
  const working = documents.filter(knowledgeJobActive).length;
  const warnings = documents.filter((item) => item.diagnostic.quality !== "good").length;
  const chunks = documents.reduce((total, item) => total + item.chunkCount, 0);
  return <div className="modal-backdrop"><section className="model-modal knowledge-modal">
    <header><div><span className="dialog-icon gradient"><Icon name="library" size={19}/></span><div><p>PROJECT KNOWLEDGE</p><h2>{project.name} · 知识库</h2></div></div><div className="knowledge-header-actions"><button type="button" className="knowledge-add" disabled={importing} onClick={() => void addDocuments()}><Icon name={importing ? "refresh" : "plus"} size={15}/>{importing ? "正在导入" : "新增文件"}</button><button type="button" className="close" onClick={close}><Icon name="close"/></button></div></header>
    <div className="knowledge-body">
      <div className="knowledge-summary"><div><span>文档总数</span><b>{documents.length}</b></div><div><span>可检索</span><b>{ready}</b></div><div><span>索引片段</span><b>{chunks.toLocaleString()}</b></div>{working > 0 ? <div className="knowledge-working"><Icon name="refresh" size={13}/><span>{working} 个任务处理中</span></div> : warnings > 0 ? <div className="knowledge-working warning"><Icon name="search" size={13}/><span>{warnings} 个文档需要核对</span></div> : null}</div>
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
      <div className="knowledge-list">{loading ? <div className="knowledge-empty"><Icon name="refresh" size={24}/><b>正在读取项目知识库</b></div> : documents.length === 0 ? <div className="knowledge-empty"><span><Icon name="library" size={27}/></span><b>知识库还是空的</b><p>添加论文、数据表或研究笔记，之后可以在当前项目的任意会话中统一检索。</p><button type="button" onClick={() => void addDocuments()}><Icon name="plus" size={14}/> 新增文件</button></div> : documents.map((item) => <div className="knowledge-entry" key={item.id}><div className="knowledge-row">
        <span className={`knowledge-file-icon quality-${item.diagnostic.quality}`}><Icon name="skill" size={17}/></span><div className="knowledge-file"><b title={item.title}>{item.title}</b><small>{documentKind(item.title)} · {item.diagnostic.summary || item.chunkingVersion}</small>{(item.errorMessage || item.job?.errorMessage) && <em title={item.errorMessage || item.job?.errorMessage}>{item.errorMessage || item.job?.errorMessage}</em>}</div>
        <div className="knowledge-task"><span className={`knowledge-status ${item.status} ${item.job?.status ?? ""}`}>{knowledgeJobActive(item) && <Icon name="refresh" size={11}/>} {knowledgeDisplayStatus(item)}</span>{knowledgeJobActive(item) && <div className="knowledge-progress"><i style={{ width: `${item.progress}%` }}/><small>{item.progress}%</small></div>}{item.job && <small>第 {item.job.attemptCount} 次 · {compactDate(item.job.updatedAt)}</small>}</div>
        <div className="knowledge-chunks"><b>{item.chunkCount.toLocaleString()}</b><small>chunks</small></div>
        <div className="knowledge-actions"><button type="button" aria-label={`查看 ${item.title} 解析诊断`} title={`${qualityText[item.diagnostic.quality]}：${item.diagnostic.summary}`} onClick={() => setDiagnosticOpen((current) => current === item.id ? "" : item.id)}><Icon name="search" size={14}/></button>{knowledgeJobActive(item) ? <button type="button" className={`danger ${taskAction === `cancel:${item.id}` ? "loading" : ""}`} aria-label={`取消 ${item.title} 索引任务`} title="取消索引任务" disabled={Boolean(taskAction)} onClick={() => void controlDocument("cancel", item)}><Icon name={taskAction === `cancel:${item.id}` ? "refresh" : "stop"} size={13}/></button> : item.status === "failed" || item.job?.status === "failed" || item.job?.status === "cancelled" ? <button type="button" className={taskAction === `retry:${item.id}` ? "loading" : ""} aria-label={`重试 ${item.title}`} title="重试失败任务" disabled={Boolean(taskAction)} onClick={() => void controlDocument("retry", item)}><Icon name="refresh" size={14}/></button> : <button type="button" className={taskAction === `rebuild:${item.id}` ? "loading" : ""} aria-label={`重建 ${item.title} 索引`} title="重新建立此文档索引" disabled={Boolean(taskAction)} onClick={() => void controlDocument("rebuild", item)}><Icon name="refresh" size={14}/></button>}<button type="button" className={`danger ${removing === item.id ? "loading" : ""}`} aria-label={`将 ${item.title} 移出知识库`} title={knowledgeJobActive(item) ? "请先取消索引任务" : "移出知识库但保留原附件"} disabled={Boolean(removing) || knowledgeJobActive(item)} onClick={() => void removeDocument(item)}><Icon name={removing === item.id ? "refresh" : "trash"} size={14}/></button></div>
      </div>{diagnosticOpen === item.id && <div className="knowledge-diagnostic"><header><span className={`quality-dot ${item.diagnostic.quality}`}/><b>{qualityText[item.diagnostic.quality]}</b><small>{item.diagnostic.parser || `parser schema v${item.parserSchemaVersion}`}</small></header><div><span>提取字符 <b>{item.diagnostic.extractedRunes.toLocaleString()}</b></span><span>结构单元 <b>{item.diagnostic.unitCount.toLocaleString()}</b></span>{item.diagnostic.pages ? <span>PDF 页面 <b>{item.diagnostic.textPages}/{item.diagnostic.pages}</b></span> : null}{item.diagnostic.headings ? <span>标题 <b>{item.diagnostic.headings}</b></span> : null}{item.diagnostic.tables ? <span>表格 <b>{item.diagnostic.tables}</b></span> : null}{item.diagnostic.sheets ? <span>Sheet <b>{item.diagnostic.sheets}</b></span> : null}</div>{item.diagnostic.warnings.length > 0 ? <ul>{item.diagnostic.warnings.map((warning) => <li key={warning}>{warning}</li>)}</ul> : <p>当前解析结果具备可检索文本和稳定来源定位。</p>}</div>}</div>)}</div>
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

  async function enableAllSkills() {
    if (!project) return;
    setBusy(true); setFeedback("");
    try {
      const result = await backend<EnableAllProjectSkillsResult>("SkillFacade", "EnableAllProjectSkills", project.id);
      await load();
      showToast("项目 Skills 已全部启用", `新启用 ${result.enabled} · 已启用 ${result.alreadyEnabled}${result.skipped ? ` · 跳过 ${result.skipped}` : ""}`);
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
          <div className="skill-catalog-actions"><button className="skill-enable-all" onClick={() => void enableAllSkills()} disabled={busy || !project}><Icon name="check" size={14}/> 启用全部</button><button className="skill-refresh" onClick={() => void refreshCatalog()} disabled={busy}><Icon name="refresh" size={14}/> 刷新目录</button></div>
          <div className="profile-caption">已安装 · {groups.length}</div>
          {loading ? <p className="skill-side-empty">正在读取 Skill 目录…</p> : groups.length ? groups.map((group) => {
            const link = projectSkills.find((item) => item.skillId === group.id);
            const representative = group.versions.find((item) => item.manifest.version === link?.version) ?? group.versions[0];
            if (!representative) return null;
            const healthy = group.versions.some((item) => item.availability === "available");
            const status = !healthy ? "failed" : link?.enabled ? "ready" : "";
            return <button key={group.id} className={`profile-item skill-profile ${!installOpen && group.id === selectedSkillId ? "selected" : ""}`} onClick={() => { setInstallOpen(false); setSelectedSkillId(group.id); setFeedback(""); }} disabled={busy}><span className="provider-logo"><Icon name="skill" size={15}/></span><span><b>{representative.manifest.name}</b><small>{group.versions.length} 个版本{link ? ` · ${link.enabled ? "当前项目已启用" : "当前项目已禁用"}` : " · 当前项目未启用"}</small></span><i className={`status-dot ${status}`}/></button>;
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
        .then((value) => setCapabilities(normalizeMCPCapabilities(value)))
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
        timeoutSeconds: current?.timeoutSeconds ?? 300,
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
      setCapabilities(normalizeMCPCapabilities(await backend<MCPCapabilities>("MCPFacade", "GetMCPCapabilities", id)));
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
const requestUsageRate = (item: RequestUsage) => {
  if (!item.cacheDetailsReported) return "未报告";
  const input = item.freshInputTokens + item.cachedInputTokens + item.cacheWriteTokens;
  return input > 0 ? `${(item.cachedInputTokens / input * 100).toFixed(1)}%` : "0.0%";
};
const requestDuration = (millis: number) => millis <= 0 ? "--" : millis < 1000 ? `${millis} ms` : `${(millis / 1000).toFixed(millis < 10_000 ? 1 : 0)} s`;
const requestKindLabel = (kind: RequestUsage["requestKind"]) => kind === "compaction" ? "上下文压缩" : kind === "image_probe" ? "图片能力探测" : kind === "multimodal_fallback" ? "识图兜底" : kind === "legacy" ? "历史记录" : "对话";
const protocolLabel = (protocol: APIProtocol) => protocol === "openai_responses" ? "Responses" : protocol === "anthropic_messages" ? "Anthropic" : "Chat Completions";
const requestStatusLabel = (item: RequestUsage) => item.statusCode >= 200 && item.statusCode < 300 ? "成功" : item.statusCode === 499 ? "已中断" : "失败";
const requestStatusClass = (item: RequestUsage) => item.statusCode >= 200 && item.statusCode < 300 ? "success" : item.statusCode === 499 ? "interrupted" : "failed";

function UsageDashboard({ profiles, close }: { profiles: Profile[]; close: () => void }) {
  const [range, setRange] = useState<"all" | "today" | "7d" | "14d" | "30d" | "custom">("today");
  const [customStart, setCustomStart] = useState("");
  const [customEnd, setCustomEnd] = useState("");
  const [todayStart, setTodayStart] = useState("00:00");
  const [todayEnd, setTodayEnd] = useState("23:59");
  const [profileFilter, setProfileFilter] = useState("");
  const [modelFilter, setModelFilter] = useState("");
  const [statusFilter, setStatusFilter] = useState("");
  const [requestDetailId, setRequestDetailId] = useState("");
  const [data, setData] = useState<UsageDashboardData | null>(null);
  const [requests, setRequests] = useState<UsageRequestPage | null>(null);
  const [requestOffset, setRequestOffset] = useState(0);
  const [reloadKey, setReloadKey] = useState(0);
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
    return { startDate, endDate, startTime: range === "today" ? todayStart : "", endTime: range === "today" ? todayEnd : "", modelProfileId: profileFilter, modelId: modelFilter };
  }, [customEnd, customStart, modelFilter, profileFilter, range, todayEnd, todayStart]);

  useEffect(() => { setRequestOffset(0); setRequestDetailId(""); }, [query, statusFilter]);
  useEffect(() => {
    let active = true;
    setLoading(true); setError("");
    Promise.all([
      backend<UsageDashboardData>("ChatFacade", "GetUsageDashboard", query),
      backend<UsageRequestPage>("ChatFacade", "GetUsageRequests", { ...query, statusCode: statusFilter ? Number(statusFilter) : 0, offset: requestOffset, limit: 20 }),
    ]).then(([dashboard, requestPage]) => {
      if (!active) return;
      setData(dashboard); setRequests(requestPage);
    }).catch((reason) => { if (active) setError(errorText(reason)); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [query, reloadKey, requestOffset, statusFilter]);

  const modelOptions = useMemo(() => Array.from(new Set(profiles.flatMap((profile) => profile.models.map((model) => model.id)))).sort(), [profiles]);
  const summary = data?.summary;
  const maxDaily = Math.max(1, ...(data?.daily.map((item) => item.realTotalTokens) ?? [1]));
  const requestPage = requests ? Math.floor(requests.offset / requests.limit) + 1 : 1;
  const requestPages = requests ? Math.max(1, Math.ceil(requests.total / requests.limit)) : 1;
  const requestDetail = requests?.items.find((item) => item.id === requestDetailId);

  return <div className="modal-backdrop"><section className="usage-modal" role="dialog" aria-modal="true" aria-labelledby="usage-title">
    <header><div><span className="dialog-icon gradient"><Icon name="chart"/></span><div><p>CLIENT-WIDE ANALYTICS</p><h2 id="usage-title">用量与缓存统计</h2></div></div><button className="close" onClick={close}><Icon name="close"/></button></header>
    <div className="usage-content">
      <div className="usage-filterbar">
        <div className="usage-presets">{(["today", "7d", "14d", "30d", "all", "custom"] as const).map((item) => <button type="button" className={range === item ? "active" : ""} onClick={() => setRange(item)} key={item}>{item === "all" ? "全部" : item === "today" ? "今天" : item === "7d" ? "近 7 天" : item === "14d" ? "近 14 天" : item === "30d" ? "近 30 天" : "自定义"}</button>)}</div>
        <div className="usage-dimensions"><label>API 配置<select value={profileFilter} onChange={(event) => setProfileFilter(event.target.value)}><option value="">全部配置</option>{profiles.map((profile) => <option value={profile.id} key={profile.id}>{profile.name}</option>)}</select></label><label>模型<select value={modelFilter} onChange={(event) => setModelFilter(event.target.value)}><option value="">全部模型</option>{modelOptions.map((model) => <option value={model} key={model}>{model}</option>)}</select></label><button type="button" className="usage-refresh" onClick={() => setReloadKey((value) => value + 1)} title="刷新"><Icon name="refresh" size={15}/></button></div>
      </div>
      {range === "today" && <div className="usage-time-range"><span>今天的统计时段</span><label>开始<input type="time" value={todayStart} onChange={(event) => setTodayStart(event.target.value)}/></label><i>至</i><label>结束<input type="time" value={todayEnd} onChange={(event) => setTodayEnd(event.target.value)}/></label><button type="button" onClick={() => { setTodayStart("00:00"); setTodayEnd("23:59"); }}>恢复全天</button></div>}
      {range === "custom" && <div className="usage-custom-range"><label>开始日期<input type="date" value={customStart} onChange={(event) => setCustomStart(event.target.value)}/></label><span>至</span><label>结束日期<input type="date" value={customEnd} onChange={(event) => setCustomEnd(event.target.value)}/></label></div>}
      {error && <div className="feedback error"><span>{error}</span></div>}
      {loading && !data ? <div className="usage-page-loading">正在汇总全部模型用量…</div> : summary && <>
        <section className="usage-hero"><div className="usage-total"><span>真实总 Token</span><strong>{usageNumber(summary.realTotalTokens)}</strong><small>实际输入 + 输出 + 缓存读取 + 缓存创建</small></div><div className="hit-rate-ring" style={{ "--hit-rate": `${summary.cacheHitRate * 100}%` } as CSSProperties}><div><strong>{usageRate(summary)}</strong><span>缓存命中率</span></div></div><div className="usage-context"><span>{summary.requestCount.toLocaleString()} 个模型请求</span><span>{summary.successfulRequests.toLocaleString()} 成功 · {summary.failedRequests.toLocaleString()} 失败</span><small>{summary.cacheDataAvailable ? `${summary.cacheReportedTurns} 个成功请求返回了缓存明细` : "成功请求尚未返回缓存明细"} · 成功率 {(summary.successRate * 100).toFixed(1)}%</small></div></section>
        <section className="usage-token-grid"><article><span>实际输入</span><b>{usageNumber(summary.freshInputTokens)}</b><small>排除缓存后的新输入</small></article><article><span>模型输出</span><b>{usageNumber(summary.outputTokens)}</b><small>模型生成 Token</small></article><article className="reasoning-token"><span>其中推理</span><b>{usageNumber(summary.reasoningTokens)}</b><small>供应商明确报告的推理 Token</small></article><article className="cache-read"><span>缓存读取</span><b>{usageNumber(summary.cacheReadTokens)}</b><small>本次命中的输入缓存</small></article><article className="cache-create"><span>缓存创建</span><b>{usageNumber(summary.cacheCreationTokens)}</b><small>写入供后续复用的缓存</small></article></section>
        <div className="usage-panels">
          <section className="usage-panel"><div className="usage-panel-title"><div><h3>日期趋势</h3><p>按系统本地日期汇总真实 Token</p></div></div>{data.daily.length ? <div className="usage-trend">{data.daily.map((item) => <div className="trend-row" key={item.date}><time>{item.date}</time><div className="trend-track"><i style={{ width: `${Math.max(2, item.realTotalTokens / maxDaily * 100)}%` }}/></div><b>{usageNumber(item.realTotalTokens)}</b><span>{usageRate(item)}</span></div>)}</div> : <div className="usage-empty">所选日期范围内还没有用量记录</div>}</section>
          <section className="usage-panel model-usage-panel"><div className="usage-panel-title"><div><h3>按模型统计</h3><p>百分比由各模型 Token 汇总后独立计算</p></div></div>{data.models.length ? <div className="usage-table"><div className="usage-table-head"><span>模型 / API</span><span>实际输入</span><span>缓存读取</span><span>输出</span><span>推理</span><span>命中率</span></div>{data.models.map((item) => <div className="usage-table-row" key={`${item.modelProfileId}\t${item.modelId}`}><span><b>{item.modelId}</b><small>{item.profileName || "未知配置"}</small></span><span>{usageNumber(item.freshInputTokens)}</span><span>{usageNumber(item.cacheReadTokens)}</span><span>{usageNumber(item.outputTokens)}</span><span>{usageNumber(item.reasoningTokens)}</span><span className={item.cacheDataAvailable ? "rate" : "muted"}>{usageRate(item)}</span></div>)}</div> : <div className="usage-empty">没有匹配的模型记录</div>}</section>
        </div>
        <section className="usage-request-panel">
          <div className="usage-panel-title"><div><h3>请求明细</h3><p>每个逻辑模型请求只记录最终结果；内部自动重试不会重复累计 Token</p></div><div className="usage-request-tools"><label>状态<select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value)}><option value="">全部</option><option value="200">200 成功</option><option value="400">400 请求错误</option><option value="401">401 鉴权失败</option><option value="404">404 未找到</option><option value="408">408 超时</option><option value="429">429 限流</option><option value="499">499 已中断</option><option value="500">500 内部错误</option><option value="502">502 响应异常</option><option value="503">503 服务不可用</option></select></label><span>{requests?.total.toLocaleString() ?? 0} 条</span></div></div>
          {requests?.items.length ? <div className="usage-request-table"><div className="usage-request-head"><span>时间</span><span>模型 / API</span><span>请求</span><span>输入总计</span><span>实际输入</span><span>缓存读取</span><span>缓存创建</span><span>输出</span><span>推理</span><span>命中率</span><span>耗时</span><span>状态</span></div>{requests.items.map((item) => { const started = new Date(item.startedAt); return <div className="usage-request-row" key={item.id}><span className="request-time"><b>{started.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false })}</b><small>{started.toLocaleDateString("zh-CN")}</small></span><span className="request-model"><b title={item.modelId}>{item.modelId}</b><small title={item.profileName}>{item.profileName || "未知配置"}</small></span><span className="request-kind"><b>{requestKindLabel(item.requestKind)}</b><small>{protocolLabel(item.apiProtocol)}</small></span><span>{usageNumber(item.inputTokens)}</span><span>{usageNumber(item.freshInputTokens)}</span><span className="cache-value">{usageNumber(item.cachedInputTokens)}</span><span>{usageNumber(item.cacheWriteTokens)}</span><span>{usageNumber(item.outputTokens)}</span><span>{usageNumber(item.reasoningTokens)}</span><span className={item.cacheDetailsReported ? "rate" : "muted"}>{requestUsageRate(item)}</span><span>{requestDuration(item.durationMillis)}</span><span className={`request-status ${requestStatusClass(item)}`}><b>{requestStatusLabel(item)}</b><button type="button" onClick={() => setRequestDetailId((value) => value === item.id ? "" : item.id)}>{item.errorMessage ? "详情" : item.statusCode}</button></span></div>; })}</div> : <div className="usage-empty">当前筛选范围内没有请求明细</div>}
          {requestDetail && <div className="usage-request-detail"><header><div><b>{requestStatusLabel(requestDetail)} · HTTP/客户端状态 {requestDetail.statusCode}</b><small>{requestDetail.errorCode || "请求已正常完成"}</small></div><button type="button" onClick={() => setRequestDetailId("")}><Icon name="close" size={13}/></button></header><div><span>传输方式 <b>{requestDetail.isStreaming ? "流式" : "非流式"}</b></span><span>首 Token <b>{requestDetail.firstTokenMillis == null ? "未观测" : requestDuration(requestDetail.firstTokenMillis)}</b></span><span>总耗时 <b>{requestDuration(requestDetail.durationMillis)}</b></span></div>{requestDetail.errorMessage && <pre>{requestDetail.errorMessage}</pre>}</div>}
          {requests && requests.total > requests.limit && <div className="usage-pagination"><span>第 {requestPage} / {requestPages} 页</span><button type="button" disabled={requests.offset <= 0} onClick={() => setRequestOffset(Math.max(0, requestOffset - requests.limit))}>上一页</button><button type="button" disabled={requests.offset + requests.limit >= requests.total} onClick={() => setRequestOffset(requestOffset + requests.limit)}>下一页</button></div>}
        </section>
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
  const [toast, setToast] = useState<{ id: number; text: string; detail: string } | null>(null);
  const [saving, setSaving] = useState(false);
  const [visionFallbackOpen, setVisionFallbackOpen] = useState(false);
  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(null), 2800);
    return () => window.clearTimeout(timer);
  }, [toast]);
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
      setToast({
        id: Date.now(),
        text: "模型配置已保存",
        detail: `${saved.name} · ${profileModels.length} 个模型`,
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
        className="model-modal model-settings-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="model-title"
      >
        {toast && <div className="mcp-toast" role="status"><span><Icon name="check" size={15}/></span><div><b>{toast.text}</b><small>{toast.detail}</small></div></div>}
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
          <aside className="model-settings-sidebar">
            <button
              className={`add-profile ${!id && !visionFallbackOpen ? "selected" : ""}`}
              onClick={() => { setVisionFallbackOpen(false); setId(""); }}
            >
              <Icon name="plus" /> 添加 API 配置
            </button>
            <div className="profile-caption">已保存</div>
            {profiles.map((profile) => (
              <button
                className={`profile-item ${profile.id === id ? "selected" : ""}`}
                onClick={() => { setVisionFallbackOpen(false); setId(profile.id); }}
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
            <button type="button" className={`vision-fallback-entry ${visionFallbackOpen ? "selected" : ""}`} onClick={() => setVisionFallbackOpen(true)}>
              <span className="provider-logo"><Icon name="model" size={15}/></span>
              <span><b>识图兜底</b><small>配置自定义多模态模型</small></span>
              <Icon name="back" size={13}/>
            </button>
          </aside>
          {visionFallbackOpen ? <VisionFallbackSettings/> : <form onSubmit={(event) => void save(event)}>
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
          </form>}
        </div>
      </section>
    </div>
  );
}

type VisionFallbackDraft = {
  name: string;
  apiProtocol: APIProtocol;
  baseUrl: string;
  modelId: string;
  apiKey: string;
  priority: number;
  enabled: boolean;
  timeoutSeconds: number;
  maxTokens: number;
};

const newVisionFallbackDraft = (): VisionFallbackDraft => ({
  name: "",
  apiProtocol: "openai_chat_completions",
  baseUrl: "",
  modelId: "",
  apiKey: "",
  priority: 100,
  enabled: true,
  timeoutSeconds: 60,
  maxTokens: 4096,
});

function VisionFallbackSettings() {
  const [channels, setChannels] = useState<VisionFallbackChannel[]>([]);
  const [editingId, setEditingId] = useState("");
  const [draft, setDraft] = useState<VisionFallbackDraft>(newVisionFallbackDraft);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [testingId, setTestingId] = useState("");
  const [feedback, setFeedback] = useState<{ kind: "ok" | "error" | "info"; text: string } | null>(null);

	const selected = channels.find((channel) => channel.id === editingId);

  const load = useCallback(async () => {
    const values = await backend<VisionFallbackChannel[]>("ModelFacade", "ListVisionFallbackChannels");
    setChannels(values ?? []);
  }, []);

  useEffect(() => {
    setLoading(true);
    load().catch((error: unknown) => setFeedback({ kind: "error", text: errorText(error) })).finally(() => setLoading(false));
  }, [load]);

  function choose(channel?: VisionFallbackChannel) {
    if (!channel) {
      setEditingId("");
      setDraft(newVisionFallbackDraft());
    } else {
      setEditingId(channel.id);
      setDraft({ name: channel.name, apiProtocol: channel.apiProtocol, baseUrl: channel.baseUrl ?? "", modelId: channel.modelId, apiKey: "", priority: channel.priority, enabled: channel.enabled, timeoutSeconds: channel.timeoutSeconds || 60, maxTokens: channel.maxTokens || 4096 });
    }
    setFeedback(null);
  }

  function update<K extends keyof VisionFallbackDraft>(key: K, value: VisionFallbackDraft[K]) {
    setDraft((current) => ({ ...current, [key]: value }));
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setFeedback({ kind: "info", text: "正在保存识图渠道…" });
    try {
      const saved = await backend<VisionFallbackChannel>("ModelFacade", "SaveVisionFallbackChannel", { id: editingId, ...draft });
      await load();
      setEditingId(saved.id);
      setDraft((current) => ({ ...current, apiKey: "" }));
      setFeedback({ kind: "ok", text: `${saved.name} 已保存，将按优先级参与图片识别。` });
    } catch (error) {
      setFeedback({ kind: "error", text: errorText(error) });
    } finally {
      setBusy(false);
    }
  }

  async function test(channelId: string) {
    setTestingId(channelId);
    setFeedback({ kind: "info", text: "正在发送测试图片…" });
    try {
      await backend<void>("ModelFacade", "TestVisionFallbackChannel", channelId);
      setFeedback({ kind: "ok", text: "测试成功，该渠道能够接收图片像素。" });
    } catch (error) {
      setFeedback({ kind: "error", text: errorText(error) });
    } finally {
      setTestingId("");
    }
  }

  async function remove() {
    if (!editingId || !window.confirm("删除这个自定义识图渠道及其系统凭据？")) return;
    setBusy(true);
    try {
      await backend<void>("ModelFacade", "DeleteVisionFallbackChannel", editingId);
      choose();
      await load();
      setFeedback({ kind: "ok", text: "自定义识图渠道已删除。" });
    } catch (error) {
      setFeedback({ kind: "error", text: errorText(error) });
    } finally {
      setBusy(false);
    }
  }

  return <div className="vision-fallback-settings">
    <header className="vision-fallback-intro"><span><Icon name="model" size={21}/></span><div><p>VISION FALLBACK</p><h3>识图兜底</h3><small>图片始终先发送给当前对话模型。只有模型明确拒绝图片或返回像素不可用标记时，才调用这里的多模态模型；普通超时、鉴权失败和限流不会触发兜底。</small></div></header>
		<section className="custom-vision-section">
			<div className="vision-section-heading"><div><h4>自定义识图渠道</h4><p>程序不内置模型。添加支持图片的 API，多个渠道按优先级从小到大依次尝试</p></div><button type="button" onClick={() => choose()} disabled={busy}><Icon name="plus" size={14}/> 添加渠道</button></div>
			<div className="vision-custom-layout">
				<div className="vision-channel-list">{loading ? <div className="vision-loading">正在读取渠道…</div> : channels.length ? channels.map((channel) => <button type="button" key={channel.id} className={channel.id === editingId ? "selected" : ""} onClick={() => choose(channel)} disabled={busy}><i className={`status-dot ${channel.enabled ? "ready" : ""}`}/><span><b>{channel.name}</b><small>{channel.modelId} · 优先级 {channel.priority}</small></span><em>{channel.enabled ? "已启用" : "已停用"}</em></button>) : <div className="vision-empty"><Icon name="model" size={18}/><span>尚未配置识图渠道。文本模型拒图后将无法兜底，请点击“添加渠道”填写多模态模型 API。</span></div>}</div>
        <form className="vision-channel-editor" onSubmit={(event) => void save(event)}>
          <div className="vision-editor-title"><div><b>{editingId ? "编辑渠道" : "新增渠道"}</b><small>{selected?.secretConfigured ? `API Key 已保存 ${selected.secretMasked ?? ""}` : "API Key 可按服务要求填写"}</small></div><label className="vision-enabled"><input type="checkbox" checked={draft.enabled} onChange={(event) => update("enabled", event.target.checked)} disabled={busy}/><span>{draft.enabled ? "启用" : "停用"}</span></label></div>
          <div className="form-row two"><label>渠道名称<input value={draft.name} onChange={(event) => update("name", event.target.value)} placeholder="例如：实验室视觉模型" required disabled={busy}/></label><label>接口协议<select value={draft.apiProtocol} onChange={(event) => update("apiProtocol", event.target.value as APIProtocol)} disabled={busy}><option value="openai_chat_completions">OpenAI Chat Completions</option><option value="openai_responses">OpenAI Responses</option><option value="anthropic_messages">Anthropic Messages</option></select></label></div>
          <label>Base URL<input value={draft.baseUrl} onChange={(event) => update("baseUrl", event.target.value)} placeholder="https://api.example.com/v1" required disabled={busy}/></label>
          <div className="form-row two"><label>Model ID<input value={draft.modelId} onChange={(event) => update("modelId", event.target.value)} placeholder="vision-model" required disabled={busy}/></label><label>API Key <small>{selected?.secretConfigured ? "留空保持现有密钥" : "无鉴权服务可留空"}</small><input className="api-key-input" type="password" autoComplete="new-password" value={draft.apiKey} onChange={(event) => update("apiKey", event.target.value)} placeholder={selected?.secretConfigured ? "留空保持现有密钥" : "sk-…"} disabled={busy}/></label></div>
          <details><summary>请求设置</summary><div className="vision-request-settings"><label>优先级<input type="number" min={0} max={1000} value={draft.priority} onChange={(event) => update("priority", Math.max(0, Math.min(1000, Number(event.target.value))))} disabled={busy}/></label><label>超时（秒）<input type="number" min={5} max={600} value={draft.timeoutSeconds} onChange={(event) => update("timeoutSeconds", Math.max(5, Math.min(600, Number(event.target.value))))} disabled={busy}/></label><label>最大输出 Token<input type="number" min={1} max={32000} value={draft.maxTokens} onChange={(event) => update("maxTokens", Math.max(1, Math.min(32000, Number(event.target.value))))} disabled={busy}/></label></div></details>
          {feedback && <div className={`feedback ${feedback.kind}`}>{feedback.kind === "ok" && <Icon name="check" size={14}/>}<span>{feedback.text}</span></div>}
          <footer><button type="button" className="danger" onClick={() => void remove()} disabled={busy || !editingId}><Icon name="trash" size={13}/> 删除</button><span/><button type="button" onClick={() => void test(editingId)} disabled={busy || !editingId || testingId === editingId}>{testingId === editingId ? "测试中…" : "测试图片"}</button><button className="primary" disabled={busy || !draft.name.trim() || !draft.baseUrl.trim() || !draft.modelId.trim()}>{busy ? "保存中…" : "保存渠道"}</button></footer>
        </form>
      </div>
    </section>
  </div>;
}
