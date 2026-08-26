import { CSSProperties, FormEvent, memo, ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { eventsOn, minimiseWindow, onFileDrop, quitApplication, setClipboardText, toggleMaximiseWindow } from "./lib/wailsRuntime";
import { createLatestRequestGate, skillTreePrefix } from "./skillSourceTree.js";
import { deriveWorkflowRunEvidence, WorkflowEvidenceNode } from "./workflowRunEvidence.js";

type Project = { id: string; name: string; description: string; workspacePath: string; workspaceKind: "managed" | "external" };
type PermissionMode = "plan" | "full_access";
type WorkspaceMode = "chat" | "research";
type ReasoningLevel = "low" | "medium" | "high" | "xhigh" | "max";
type APIProtocol = "openai_chat_completions" | "openai_responses" | "anthropic_messages";
type Conversation = { id: string; projectId: string; title: string; modelProfileId: string; modelId: string; permissionMode: PermissionMode; reasoningLevel: ReasoningLevel };
type AttachmentReference = { attachmentId: string; originalName: string; mimeType: string; format: string; sizeBytes: number; unitCount: number; truncated: boolean };
type MessagePart = { type: string; text?: string; payload?: AttachmentReference };
type Citation = { id: string; messageId: string; runId: string; toolCallId: string; projectId: string; reference: string; ordinal: number; indexVersionId: string; documentId: string; attachmentId: string; chunkId: string; sourceName: string; mimeType?: string; locator: string; title?: string; quote: string; quoteSha256: string; sourceStart: number; sourceEnd: number; bibliographyId?: string; bibliography?: ResearchBibliographySnapshot; evidenceLevel?: ResearchEvidenceLevel; createdAt: string };
type MessageReasoning = { status: string; requestedLevel: ReasoningLevel; resolvedLevel?: ReasoningLevel; observed: boolean; signatureObserved: boolean; tokens: number; summary?: string };
type Message = { id: string; runId?: string; role: "user" | "assistant" | "system" | "tool"; status: string; parts: MessagePart[]; citations?: Citation[]; reasoning?: MessageReasoning };
type Attachment = { id: string; projectId: string; originalName: string; mimeType: string; format: string; sizeBytes: number; sha256: string; status: "parsing" | "ready" | "failed"; unitCount: number; extractedRunes: number; truncated: boolean; errorMessage?: string };
type AttachmentImportBatch = { attachments: Attachment[]; errors: { path: string; message: string }[] };
type ResearchSource = { id: string; name: string; domain: string; description: string; homepage: string; host: string; keyFree: boolean; fullText: boolean };
type ResearchAuthor = { name: string; orcid?: string; raw?: string; source?: string };
type ResearchIdentifiers = { doi?: string; pmid?: string; pmcid?: string; arxiv?: string; openAlex?: string; semanticScholar?: string };
type ResearchWork = { sourceId: string; sourceRecordId: string; title: string; abstract?: string; authors: ResearchAuthor[]; year?: number; published?: string; venue?: string; volume?: string; issue?: string; pages?: string; publisher?: string; workType?: string; language?: string; identifiers: ResearchIdentifiers; landingUrl?: string; pdfUrl?: string; openAccess: boolean; citedByCount?: number; score?: number };
type ResearchSourceStatus = { sourceId: string; status: "ok" | "empty" | "failed"; count: number; errorCode?: string; message?: string; retryable: boolean };
type ResearchQuery = { id: string; projectId: string; text: string; sourceIds: string[]; limitPerSource: number; sources: ResearchSourceStatus[]; partial: boolean; resultCount: number; createdAt: string; updatedAt: string };
type ResearchSourceRecord = { id: string; projectId: string; work: ResearchWork; firstSeenAt: string; updatedAt: string };
type ResearchReviewStatus = "pending" | "included" | "excluded";
type ResearchImportStatus = "not_imported" | "importing" | "imported" | "failed";
type ResearchImportKind = "full_text" | "metadata_abstract";
type ResearchCandidate = { id: string; projectId: string; candidateKey: string; reviewStatus: ResearchReviewStatus; exclusionReason?: string; note?: string; importStatus: ResearchImportStatus; importKind?: ResearchImportKind; attachmentId?: string; importError?: string; preferred: ResearchWork; records: ResearchSourceRecord[]; aliases: { kind: string; value: string }[]; createdAt: string; updatedAt: string };
type ResearchCandidatePage = { items: ResearchCandidate[]; total: number; offset: number; limit: number };
type ResearchSearchResult = { query: ResearchQuery; page: ResearchCandidatePage };
type ResearchImportResult = { candidate: ResearchCandidate; attachment: Attachment };
type ResearchBibliographicAuthor = { name: string; orcid?: string };
type ResearchBibliographyData = { authors: ResearchBibliographicAuthor[]; year?: number; title?: string; published?: string; containerTitle?: string; volume?: string; issue?: string; pages?: string; publisher?: string; doi?: string; pmid?: string; pmcid?: string; arxiv?: string; openAlex?: string; url?: string; workType?: string; language?: string };
type ResearchBibliographySnapshot = { schemaVersion: number; bibliographyId: string; revision: number; data: ResearchBibliographyData; capturedAt: string };
type ResearchBibliographyFieldSource = { id: string; field: string; sourceRecordId?: string; sourceIdSnapshot: string; sourceRecordIdSnapshot: string; value: unknown; valueSha256: string; observedAt: string };
type ResearchBibliographyRevision = { id: string; revision: number; field: string; previous: unknown; next: unknown; sourceKind: "user_edit" | "source_selection"; sourceRecordIdSnapshot?: string; reason?: string; createdAt: string };
type ResearchEvidenceLevel = "none" | "full_text" | "metadata_abstract";
type ResearchBibliographyMaterial = { id: string; attachmentId?: string; knowledgeDocumentId?: string; attachmentIdSnapshot: string; knowledgeDocumentIdSnapshot?: string; attachmentSha256Snapshot: string; importKind: ResearchImportKind; evidenceLevel: ResearchEvidenceLevel; createdAt: string; updatedAt: string };
type ResearchBibliography = { id: string; projectId: string; candidateId: string; revision: number; data: ResearchBibliographyData; selectedSources: Record<string, string>; fieldSources: ResearchBibliographyFieldSource[]; revisions: ResearchBibliographyRevision[]; materials: ResearchBibliographyMaterial[]; createdAt: string; updatedAt: string };
type ResearchEvidenceField = "research_question" | "method" | "sample_dataset" | "finding" | "limitation" | "note";
type ResearchEvidenceReference = { indexVersionId: string; documentId: string; attachmentId: string; chunkId: string };
type ResearchEvidenceSnapshot = ResearchEvidenceReference & { sourceName: string; locator: string; quote: string; quoteSha256: string; sourceStart: number; sourceEnd: number };
type ResearchEvidenceEntry = { id: string; projectId: string; bibliographyId: string; field: ResearchEvidenceField; content: string; provenance: "user" | "model"; reviewStatus: "pending" | "verified" | "rejected"; evidenceLevel: ResearchEvidenceLevel; evidence?: ResearchEvidenceSnapshot; createdAt: string; updatedAt: string };
type ResearchEvidenceSearchMatch = { reference: ResearchEvidenceReference; sourceName: string; locator: string; title?: string; snippet: string; rank: number };
type ArtifactKind = "document" | "data" | "image" | "code" | "other";
type ArtifactStatus = "active" | "trashed";
type ArtifactProvenance = { schemaVersion: number; projectId: string; sourceKind: "assistant_message" | "workspace_file" | "tool"; conversationId?: string; conversationTitle?: string; runId?: string; workflowRunId?: string; messageId?: string; toolCallId?: string; toolName?: string; toolVersion?: string; modelProfileId?: string; modelProfileName?: string; modelId?: string; apiProtocol?: APIProtocol; workspaceRelativePath?: string; workspaceModifiedAt?: string; skills: { id: string; version: string; contentHash: string; packageHash: string; origin?: string; dynamic?: boolean }[] };
type ArtifactLineage = { id: string; artifactVersionId: string; ordinal: number; relationKind: "run" | "workflow_run" | "message" | "tool_call" | "artifact_version" | "workspace_file"; sourceIdSnapshot: string; sourceRunId?: string; sourceWorkflowRunId?: string; sourceMessageId?: string; sourceToolCallId?: string; sourceArtifactVersionId?: string; label?: string; createdAt: string };
type ArtifactCitation = { id: string; artifactVersionId: string; ordinal: number; reference: string; sourceName: string; mimeType?: string; locator?: string; title?: string; quote: string; quoteSha256: string; bibliographyId?: string; bibliography?: ResearchBibliographySnapshot; evidenceLevel?: ResearchEvidenceLevel; createdAt: string };
type ArtifactExportFormat = "docx" | "pdf";
type ArtifactCitationStyle = "gb_t_7714_2015" | "apa_7";
type ArtifactExport = { id: string; projectId: string; artifactVersionId: string; format: ArtifactExportFormat; citationStyle: ArtifactCitationStyle; generatorVersion: string; fileName: string; mimeType: string; sizeBytes: number; sha256: string; sourceSha256: string; createdAt: string };
type ArtifactVersion = { id: string; artifactId: string; versionNumber: number; fileName: string; mimeType: string; sizeBytes: number; sha256: string; sourceKind: "assistant_message" | "workspace_file" | "tool"; provenance: ArtifactProvenance; lineage: ArtifactLineage[]; citations: ArtifactCitation[]; exports: ArtifactExport[]; createdAt: string };
type ResearchArtifact = { id: string; projectId: string; name: string; kind: ArtifactKind; status: ArtifactStatus; currentVersionId: string; currentVersion?: ArtifactVersion; createdAt: string; updatedAt: string; trashedAt?: string };
type ArtifactDetail = { artifact: ResearchArtifact; versions: ArtifactVersion[] };
type ArtifactSaveResult = { artifact: ResearchArtifact; version: ArtifactVersion; created: boolean };
type ArtifactExportResult = { export: ArtifactExport; created: boolean };
type ArtifactPreviewBlock = { kind: "heading" | "paragraph" | "list_item" | "quote" | "code" | "table" | "rule"; level?: number; text?: string; locator?: string; rows?: string[][] };
type ArtifactStructuredPreview = { title?: string; format: string; blocks: ArtifactPreviewBlock[]; metadata: Record<string, string> };
type ArtifactPreview = { versionId: string; kind: "text" | "image" | "binary" | "document"; text?: string; data?: string; mimeType: string; truncated: boolean; document?: ArtifactStructuredPreview };
type ArtifactIntegrity = { artifactId: string; versionId: string; status: "verified" | "missing" | "mismatch"; expectedSize: number; actualSize: number; expectedSha256: string; actualSha256?: string; message?: string; checkedAt: string };
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
type SkillOrigin = "default" | "installed" | "user" | "project";
type SkillCapability = "native" | "requires_dependency" | "requires_external_service" | "unavailable" | "unreviewed";
type DynamicSkill = { name: string; description: string; category?: string; tags: string[]; routingAliases: string[]; origin: SkillOrigin; entry: boolean; enabled: boolean; allowedTools: string[]; contentHash: string; packageHash: string; fileCount: number; referenceCount: number; assetCount: number; scriptCount: number; instructionRunes: number; overridden: SkillOrigin[]; namespace?: string; reviewVerdict?: string; repoUrl?: string; pinnedSha?: string; installedAt?: string; capability: SkillCapability; capabilityReason: string; capabilityAuditVersion?: string; requiredTools: string[]; missingTools: string[]; pythonPackages: string[]; cliDependencies: string[]; externalServices: string[]; capabilityLimitations: string[] };
type DynamicSkillCategory = { name: string; count: number };
type DynamicSkillSnapshot = { skills: DynamicSkill[]; categories: DynamicSkillCategory[]; diagnostics: string[]; capabilities: Partial<Record<SkillCapability, number>>; defaultCount: number; enabledCount: number };
type SkillSourceEntry = { path: string; kind: "directory" | "file"; size: number; text: boolean; mediaType?: string };
type SkillSourceTree = { name: string; origin: SkillOrigin; packageHash: string; entries: SkillSourceEntry[] };
type SkillSourceFile = { name: string; path: string; mediaType?: string; text: boolean; content: string; originalBytes: number };
type SkillPolicyBatchResult = { changed: number; unchanged: number; total: number };
type SkillInstallRecord = { namespace: string; name: string; description: string; verdict: string; packageHash: string };
type SkillReviewRejection = { name: string; reason: string };
type SkillReviewWarning = { name: string; file: string; line: number; pattern: string; snippet: string };
type SkillGitInstallResult = { namespace: string; repoUrl: string; pinnedSha: string; installed: SkillInstallRecord[]; rejected: SkillReviewRejection[]; warnings: SkillReviewWarning[]; reviewRequired: boolean; replaced: boolean; idempotent: boolean };
type SkillRemoveResult = { namespace: string; name?: string; archived: number; recoverable: boolean };
type ProjectArchiveStats = { conversations: number; runs: number; attachments: number; knowledgeDocuments: number; artifacts: number; researchCandidates: number; bibliographies: number; evidenceEntries: number };
type ProjectArchiveSkillBinding = { skillId: string; version: string; contentHash: string; packageHash: string; enabled: boolean; priority: number };
type ProjectArchiveExportResult = { path: string; sha256: string; sizeBytes: number; fileCount: number; manifest: { stats: ProjectArchiveStats; skillBindings: ProjectArchiveSkillBinding[] } };
type ProjectArchiveRestoreReport = { project: Project; sourceProjectId: string; filesRestored: number; bytesRestored: number; historicalProfiles: number; restoredSkillBindings: number; missingSkillBindings: ProjectArchiveSkillBinding[]; secretsRequireRebinding: boolean; excluded: string[] };
type PythonInterpreter = { executablePath: string; version: string; architecture: string; implementation: string; prefix: string; basePrefix: string; executableSha256: string; hasVenv: boolean; hasPip: boolean };
type PythonDiscovery = { status: "available" | "unavailable" | "cancelled"; message: string; interpreters: PythonInterpreter[] };
type PythonEnvironmentKind = "legacy_managed" | "workspace_managed" | "external";
type PythonEnvironment = { id?: string; projectId: string; state: "absent" | "creating" | "ready" | "broken" | "deleting"; environmentKind: PythonEnvironmentKind; baseExecutablePath: string; baseExecutableVersion: string; baseExecutableSha256: string; architecture: string; implementation: string; environmentPythonPath: string; environmentFingerprint: string; lock: string[]; freezeSha256: string; createdAt: string; updatedAt: string; lastVerifiedAt?: string; errorMessage?: string };
type WorkflowDataType = "any" | "string" | "number" | "integer" | "boolean" | "object" | "array" | "artifacts" | "citations";
type WorkflowNodeKind = "tool" | "shell" | "python" | "human_confirmation" | "candidate_selection" | "citation_selection";
type WorkflowPort = { name: string; type: WorkflowDataType; description?: string; fileKind?: "delimited" | "xlsx" | "tabular"; control?: "analysis_request"; minItems?: number; maxItems?: number; required: boolean; default?: unknown };
type WorkflowNode = { id: string; name: string; kind: WorkflowNodeKind; toolName?: string; arguments: Record<string, unknown>; prompt?: string };
type WorkflowEdge = { fromNode: string; fromPort: string; toNode: string; toPort: string };
type WorkflowOutput = { name: string; type: WorkflowDataType; fromNode: string; fromPort: string; required: boolean; description?: string };
type WorkflowDefinition = { schemaVersion: number; name: string; description?: string; inputs: WorkflowPort[]; nodes: WorkflowNode[]; edges: WorkflowEdge[]; outputs: WorkflowOutput[] };
type WorkflowTemplate = { id: string; name: string; description: string; definition: WorkflowDefinition };
type WorkflowDiagnostic = { severity: "error" | "warning"; code: string; path: string; message: string };
type WorkflowPreviewNode = { ordinal: number; id: string; name: string; kind: WorkflowNodeKind; toolName?: string; toolVersion?: string; risk?: string; permissions: PermissionRequirement[]; idempotent: boolean; sideEffect: boolean; summary: string };
type WorkflowPreview = { valid: boolean; definitionSha256?: string; compilationSha256?: string; diagnostics: WorkflowDiagnostic[]; nodes: WorkflowPreviewNode[]; edgeCount: number; inputCount: number; outputCount: number };
type ResearchWorkflow = { id: string; projectId: string; name: string; description?: string; currentVersionId: string; version: number; createdAt: string; updatedAt: string };
type WorkflowVersion = { id: string; workflowId: string; version: number; definition: WorkflowDefinition; definitionSha256: string; compilation: WorkflowCompilation; compilationSha256: string; runtimeInputs?: WorkflowPort[]; createdAt: string };
type WorkflowDetail = { workflow: ResearchWorkflow; versions: WorkflowVersion[] };
type WorkflowSaveResult = { workflow: ResearchWorkflow; version: WorkflowVersion; created: boolean };
type WorkflowInputFile = { relativePath: string; name: string; sizeBytes: number; sha256: string; staged: boolean };
type WorkflowRunStatus = "queued" | "running" | "waiting_approval" | "waiting_human_confirmation" | "paused" | "completed" | "failed" | "cancelled" | "interrupted";
type WorkflowStepStatus = "queued" | "running" | "waiting_approval" | "waiting_human_confirmation" | "completed" | "failed" | "cancelled" | "interrupted" | "outcome_unknown";
type WorkflowToolSnapshot = { qualifiedName: string; version: string; risk: string; permissions: PermissionRequirement[]; idempotent: boolean; inputSchema: unknown; outputSchema?: unknown };
type WorkflowCompiledNode = { id: string; name: string; kind: WorkflowNodeKind; tool?: WorkflowToolSnapshot; arguments: Record<string, unknown>; prompt?: string; dependencies: string[]; sideEffect: boolean };
type WorkflowCompilation = { schemaVersion: number; compilerVersion: string; definitionSha256: string; compilationSha256: string; order: string[]; nodes: WorkflowCompiledNode[]; edges: WorkflowEdge[]; inputs: WorkflowPort[]; outputs: WorkflowOutput[]; diagnostics: WorkflowDiagnostic[] };
type WorkflowRun = { id: string; projectId: string; workflowId: string; workflowVersionId: string; status: WorkflowRunStatus; permissionMode: PermissionMode; inputs: Record<string, unknown>; inputsSha256: string; compilation: WorkflowCompilation; compilationSha256: string; outputs: Record<string, unknown>; currentStep: number; errorCode?: string; errorMessage?: string; cancelRequested: boolean; resumeStatus?: WorkflowRunStatus; createdAt: string; startedAt?: string; completedAt?: string; updatedAt: string };
type WorkflowStep = { id: string; workflowRunId: string; nodeId: string; ordinal: number; nodeKind: WorkflowNodeKind; status: WorkflowStepStatus; attempt: number; input: Record<string, unknown>; inputSha256?: string; output: Record<string, unknown>; toolCallId?: string; idempotencyKey?: string; errorCode?: string; errorMessage?: string; startedAt?: string; completedAt?: string; updatedAt: string };
type WorkflowRuntimeEvent = { id: string; workflowRunId: string; sequence: number; type: string; payload: Record<string, unknown>; createdAt: string };
type WorkflowRunDetail = { run: WorkflowRun; steps: WorkflowStep[]; events: WorkflowRuntimeEvent[]; pendingApprovals: Approval[] };
type WorkflowCandidate = { id: string; title?: string; authors?: { name?: string }[]; year?: number; venue?: string; doi?: string; sourceIds?: string[]; openAccess?: boolean; abstract?: string };
type WorkflowCitation = { id: string; kind?: string; reference?: string; projectId?: string; indexVersionId?: string; documentId?: string; attachmentId?: string; chunkId?: string; sourceName?: string; mimeType?: string; locator?: string; title?: string; quote?: string; quoteSha256?: string; sourceStart?: number; sourceEnd?: number };
type VisionFallbackChannel = { id: string; name: string; baseUrl?: string; modelId: string; apiProtocol: APIProtocol; priority: number; enabled: boolean; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; maxTokens: number };
type Envelope = { aggregateId: string; sequence: number; type: string; payload: Record<string, unknown> };
type CreateDialog = { kind: "project" | "conversation"; title: string; description: string; workspacePath: string } | null;
type IconName = "spark" | "plus" | "chat" | "settings" | "shield" | "model" | "send" | "stop" | "play" | "pause" | "search" | "refresh" | "folder" | "check" | "close" | "back" | "trash" | "tool" | "server" | "chart" | "skill" | "paperclip" | "library" | "copy" | "archive" | "download" | "history";
type SlashCommandID = "mcp" | "skill" | "knowledge" | "compact" | "model" | "reasoning" | "permission" | "status" | "usage" | "new" | "help";
type SlashCommand = { id: SlashCommandID; name: string; title: string; description: string; icon: IconName; enabled: boolean; disabledReason?: string; state?: string; stateKind?: "on" | "off" | "loading" };
type SlashPanelMode = "mcp" | "mcp-detail" | "skill" | "knowledge" | "model" | "reasoning" | "permission" | "status" | "usage";
type SlashSkillItem = Pick<DynamicSkill, "name" | "description" | "origin" | "enabled">;
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
    send: <><path d="m22 2-7 20-4-9-9-4 20-7Z"/><path d="M22 2 11 13"/></>, stop: <rect x="6" y="6" width="12" height="12" rx="2"/>, play: <path d="m8 5 11 7-11 7V5Z"/>, pause: <><path d="M9 5v14M15 5v14"/></>, search: <><circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/></>,
    refresh: <><path d="M20 11a8 8 0 1 0-2.34 5.66"/><path d="M20 4v7h-7"/></>, folder: <path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6Z"/>,
    check: <path d="m5 12 4 4L19 6"/>, close: <><path d="m6 6 12 12M18 6 6 18"/></>, back: <><path d="m15 18-6-6 6-6"/><path d="M9 12h10"/></>, trash: <><path d="M3 6h18M8 6V4h8v2M19 6l-1 15H6L5 6M10 11v5M14 11v5"/></>, tool: <><path d="M14.7 6.3a4 4 0 0 0-5 5L3 18l3 3 6.7-6.7a4 4 0 0 0 5-5l-2.2 2.2-3-3 2.2-2.2Z"/></>, server: <><rect x="4" y="3" width="16" height="7" rx="2"/><rect x="4" y="14" width="16" height="7" rx="2"/><path d="M8 6.5h.01M8 17.5h.01M12 6.5h5M12 17.5h5"/></>,
    chart: <><path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/></>,
    skill: <><path d="M7 3h8l4 4v14H7a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Z"/><path d="M14 3v5h5M9 12h6M9 16h6"/></>,
    paperclip: <path d="m21.4 11.6-8.9 8.9a6 6 0 0 1-8.5-8.5l9.6-9.6a4 4 0 0 1 5.7 5.7l-9.6 9.6a2 2 0 1 1-2.8-2.8l8.9-8.9"/>,
    library: <><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2Z"/><path d="M8 7h8M8 11h6"/></>,
    copy: <><rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/></>,
    archive: <><path d="M4 5h16v4H4z"/><path d="M6 9v11h12V9M10 13h4"/></>,
    download: <><path d="M12 3v12M7 10l5 5 5-5"/><path d="M5 21h14"/></>,
    history: <><path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l3 2"/></>,
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
  const [artifactsOpen, setArtifactsOpen] = useState(false);
  const [researchOpen, setResearchOpen] = useState(false);
  const [pythonOpen, setPythonOpen] = useState(false);
  const [workspaceMode, setWorkspaceMode] = useState<WorkspaceMode>("chat");
  const [archiveBusy, setArchiveBusy] = useState<"" | "export" | "restore">("");
  const [archiveReport, setArchiveReport] = useState<ProjectArchiveRestoreReport | null>(null);
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
    const snapshot = await backend<DynamicSkillSnapshot>("SkillFacade", "ListSkills", projectId);
    return snapshot.skills
      .filter((item) => item.entry && item.capability !== "unavailable")
      .map(({ name, description, origin, enabled }) => ({ name, description, origin, enabled }))
      .sort((left, right) => Number(right.enabled) - Number(left.enabled) || left.name.localeCompare(right.name, "zh-CN"));
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
  useEffect(() => {
    if (workspaceMode === "research" && !selectedProject) setWorkspaceMode("chat");
  }, [workspaceMode, selectedProject]);
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
  const enabledSkillCount = slashSkills.filter((item) => item.enabled).length;
  const availableSkillCount = slashSkills.length;
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
        if (!skill.enabled) { setNotice("该 Skill 当前不允许模型加载，请先在 Skills 中启用。"); return; }
        setInput(`Use the ${skill.name} skill: `); setSlashPanel(null); window.requestAnimationFrame(() => composerInputRef.current?.focus());
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

  return <div className={`app-shell mode-${workspaceMode}`}>
	<div className="window-titlebar" onDoubleClick={(event) => { if (!(event.target instanceof Element) || !event.target.closest(".window-controls")) toggleMaximiseWindow(); }}><div className="window-brand"><span><Icon name="spark" size={13}/></span><b>SciAide</b>{workspaceMode === "research" && <em>科研模式</em>}</div><div className="window-controls"><button type="button" aria-label="最小化窗口" title="最小化" onClick={minimiseWindow}>—</button><button type="button" aria-label="最大化或还原窗口" title="最大化/还原" onClick={toggleMaximiseWindow}>□</button><button type="button" className="window-close" aria-label="关闭窗口" title="关闭" onClick={quitApplication}>×</button></div></div>
    <aside className="sidebar">
      <div className="logo"><span><Icon name="spark" size={21}/></span><div><strong>SciAide</strong><small>Research Copilot</small></div></div>
      <div className="project-create-actions"><button className="new-project" onClick={() => setCreateDialog({ kind: "project", title: "", description: "", workspacePath: "" })} disabled={Boolean(archiveBusy)}><Icon name="plus"/> 新建科研项目</button><button type="button" className="project-restore" title="从 .sciaide-project 无密钥归档恢复为新项目" aria-label="恢复项目归档" disabled={Boolean(archiveBusy)} onClick={() => void restoreProjectArchive()}><Icon name={archiveBusy === "restore" ? "refresh" : "download"} size={16}/></button></div>
      <div className="project-block"><label className="field-label" htmlFor="project">WORKSPACE</label><div className={`project-actions ${selectedProject ? "has-project" : ""}`}><div className="select-shell"><Icon name="folder" size={16}/><select id="project" value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="">选择项目</option>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></div>{selectedProject && <><button className="icon-project-action" title="导出无密钥项目归档" aria-label="导出项目归档" disabled={Boolean(archiveBusy)} onClick={() => void exportProjectArchive(selectedProject)}><Icon name={archiveBusy === "export" ? "refresh" : "archive"} size={15}/></button><button className="icon-danger" title="从 SciAide 移除项目" aria-label="从 SciAide 移除项目" disabled={Boolean(archiveBusy)} onClick={() => void removeProject(selectedProject)}><Icon name="trash" size={15}/></button></>}</div>{selectedProject && <small className="workspace-path" title={selectedProject.workspacePath}>{selectedProject.workspaceKind === "external" ? "外部目录" : "SciAide 托管"} · {selectedProject.workspacePath}</small>}</div>
      <div className="section-title"><span>研究会话</span><button aria-label="新建会话" onClick={() => setCreateDialog({ kind: "conversation", title: "", description: "", workspacePath: "" })} disabled={!projectId}><Icon name="plus" size={17}/></button></div>
      <nav className="conversation-list">{conversations.length ? conversations.map((conversation) => <div className={`conversation-row ${conversation.id === conversationId ? "active" : ""}`} key={conversation.id}><button onClick={() => setConversationId(conversation.id)}><Icon name="chat" size={16}/><span>{conversation.title}</span></button><button className="conversation-remove" title="移除会话" onClick={() => void removeConversation(conversation)}><Icon name="close" size={13}/></button></div>) : <p className="sidebar-empty">{projectId ? "还没有会话，点击右上角 ＋ 创建" : "选择项目后显示会话"}</p>}</nav>
      <div className="sidebar-footer"><button onClick={() => setUsageOpen(true)}><span className="nav-icon"><Icon name="chart" size={17}/></span><span><b>用量统计</b><small>全部模型 · 日期与缓存命中</small></span></button><button onClick={() => setSkillsOpen(true)}><span className="nav-icon"><Icon name="skill" size={17}/></span><span><b>Skills</b><small>{selectedProject ? `管理 ${selectedProject.name} 的研究技能` : "安装与管理研究技能"}</small></span></button><button onClick={() => setMcpOpen(true)}><span className="nav-icon"><Icon name="server" size={17}/></span><span><b>MCP Servers</b><small>连接科研工具与数据服务</small></span></button><button onClick={() => setSettingsOpen(true)}><span className="nav-icon"><Icon name="settings" size={17}/></span><span><b>模型与 API</b><small>{profiles.length ? `${profiles.length} 个配置可用` : "配置你的第一个模型"}</small></span><span className={selectedProfile?.secretConfigured ? "status-dot ready" : "status-dot"}/></button><div className="local-note"><Icon name="shield" size={13}/> 密钥由系统凭据库保护</div></div>
    </aside>

	<main className="workspace">
	  <header className="topbar"><div className="mode-context"><nav className="workspace-mode-switch" aria-label="工作模式"><button type="button" className={workspaceMode === "chat" ? "selected" : ""} onClick={() => setWorkspaceMode("chat")}><Icon name="chat" size={15}/>自由对话</button><button type="button" className={workspaceMode === "research" ? "selected" : ""} disabled={!selectedProject} title={selectedProject ? "进入项目科研模式" : "请先选择项目"} onClick={() => setWorkspaceMode("research")}><Icon name="history" size={15}/>科研模式</button></nav><div className="breadcrumbs"><span>{selectedProject?.name ?? "Workspace"}</span><i>/</i><strong>{workspaceMode === "research" ? "研究任务工作台" : selectedConversation?.title ?? "新研究"}</strong></div></div><div className="top-actions"><button type="button" className="research-open" aria-label="打开文献发现" title={selectedProject ? `检索并筛选 ${selectedProject.name} 的研究文献` : "请先选择项目"} disabled={!selectedProject} onClick={() => setResearchOpen(true)}><Icon name="search" size={15}/><span>文献发现</span></button><button type="button" className="python-open" aria-label="打开项目 Python 环境" title={selectedProject ? `管理 ${selectedProject.name} 的 Python 环境` : "请先选择项目"} disabled={!selectedProject} onClick={() => setPythonOpen(true)}><Icon name="tool" size={15}/><span>Python 环境</span></button><button type="button" className="artifact-open" aria-label="打开科研产物" title={selectedProject ? `查看 ${selectedProject.name} 的科研产物` : "请先选择项目"} disabled={!selectedProject} onClick={() => setArtifactsOpen(true)}><Icon name="archive" size={15}/><span>科研产物</span></button><button type="button" className="knowledge-open" aria-label="打开项目知识库" title={selectedProject ? `管理 ${selectedProject.name} 的知识库` : "请先选择项目"} disabled={!selectedProject} onClick={() => setKnowledgeOpen(true)}><Icon name="library" size={15}/><span>知识库</span></button>{workspaceMode === "chat" ? <><div className="permission-picker" title={busy ? "运行期间不能切换权限模式" : "当前 Workspace 内只读免确认；外部读取、写入和其他工具需确认"}><Icon name="shield" size={13}/><select aria-label="工具权限模式" value={selectedConversation?.permissionMode ?? "plan"} disabled={!selectedConversation || busy} onChange={(event) => void changePermissionMode(event.target.value as PermissionMode)}><option value="plan">Plan · 写入/工具确认</option><option value="full_access">Full Access</option></select></div><div className="model-picker"><span className={selectedProfile?.secretConfigured ? "status-dot ready" : "status-dot"}/><select aria-label="选择模型" value={selectedModelKey} onChange={(event) => { const [nextProfile, nextModel] = splitModelKey(event.target.value); setProfileId(nextProfile); setModelId(nextModel); }}><option value="">选择模型</option>{selectableModels.map(({ profile, model }) => <option key={modelKey(profile.id, model.id)} value={modelKey(profile.id, model.id)}>{profile.name} · {model.id}</option>)}</select></div><div className={`reasoning-picker ${reasoning.kind}`} title="参数已接受只代表服务端接受档位；收到 thinking/reasoning 块或 reasoning token 后才显示已验证。明确拒绝时逐级回退，不发送后台探测。"><Icon name="spark" size={13}/><select aria-label="思考强度" value={selectedConversation?.reasoningLevel ?? "medium"} disabled={!selectedConversation || busy} onChange={(event) => void changeReasoningLevel(event.target.value as ReasoningLevel)}>{reasoningLevels.map((level) => <option value={level} key={level}>{level}</option>)}</select><span className="reasoning-state">{reasoning.text.replace(`${selectedConversation?.reasoningLevel ?? "medium"} · `, "").replace(`${selectedConversation?.reasoningLevel ?? "medium"} `, "")}</span></div></> : <div className="research-mode-indicator"><i/><span><b>RESEARCH MODE</b><small>过程可恢复 · 证据可追溯 · 产物可复现</small></span></div>}</div></header>
      {workspaceMode === "research" && selectedProject ? <WorkflowStudio key={selectedProject.id} project={selectedProject} pythonDialogOpen={pythonOpen} openPython={() => setPythonOpen(true)} openArtifacts={() => setArtifactsOpen(true)}/> : <>
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
              saveArtifact={saveMessageArtifact}
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
          {slashPanel === "skill" && <div className="slash-runtime-list">{slashPanelLoading ? <p>正在读取项目 Skills…</p> : slashSkills.length ? slashSkills.map((skill, index) => <button type="button" className={slashSelected === index ? "selected" : ""} key={skill.name} aria-disabled={!skill.enabled} onMouseEnter={() => setSlashSelected(index)} onMouseDown={(event) => { event.preventDefault(); void executeSlashPanelSelection(index); }}><span className="slash-command-icon"><Icon name="skill" size={16}/></span><span className="slash-command-copy"><b>{skill.name} <i>{skillOriginText(skill.origin)}</i></b><small>{skill.description}</small></span><span className={`slash-command-state ${skill.enabled ? "on" : "off"}`}><i/>{skill.enabled ? "允许加载" : "已禁用"}</span></button>) : <p>当前没有可作为入口的 Skill</p>}<button type="button" className={`slash-manage ${slashSelected === slashSkills.length ? "selected" : ""}`} onMouseEnter={() => setSlashSelected(slashSkills.length)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setSkillsOpen(true); }}><Icon name="settings" size={14}/> 管理 Skills</button></div>}
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
      </>}
    </main>
    {settingsOpen && <ModelSettings profiles={profiles} close={() => setSettingsOpen(false)} refresh={loadProfiles} select={setProfileId}/>}
    {mcpOpen && <MCPSettings close={() => { setMcpOpen(false); void loadMCPStatus(); }}/>}
    {usageOpen && <UsageDashboard profiles={profiles} close={() => setUsageOpen(false)}/>}
    {skillsOpen && <SkillSettings project={selectedProject} close={() => setSkillsOpen(false)}/>}
    {knowledgeOpen && selectedProject && <KnowledgeLibrary project={selectedProject} close={() => setKnowledgeOpen(false)}/>}
    {pythonOpen && selectedProject && <PythonEnvironmentSettings project={selectedProject} close={() => setPythonOpen(false)} feedback={setNotice}/>}
    {artifactsOpen && selectedProject && <ArtifactLibrary project={selectedProject} close={() => setArtifactsOpen(false)}/>}
    {researchOpen && selectedProject && <ResearchDiscovery
      project={selectedProject}
      close={() => setResearchOpen(false)}
      openKnowledge={() => { setResearchOpen(false); setKnowledgeOpen(true); }}
    />}
    {archiveReport && (
      <ProjectArchiveReport
        report={archiveReport}
        close={() => setArchiveReport(null)}
        openModels={() => { setArchiveReport(null); setSettingsOpen(true); }}
        openSkills={() => { setArchiveReport(null); setSkillsOpen(true); }}
      />
    )}
    {createDialog && <CreateModal value={createDialog} setValue={setCreateDialog} close={() => setCreateDialog(null)} submit={submitCreate}/>}
  </div>;

  async function removeProject(value: Project) {
    const effect = value.workspaceKind === "managed" ? "托管目录会移至 ~/.sciaide/backups/trash，可手动恢复。" : "仅移除 SciAide 记录，外部目录及文件不会删除。";
    if (!window.confirm(`从 SciAide 移除“${value.name}”？\n\n${effect}\n项目下的会话和运行记录将删除。`)) return;
    try { await backend("ProjectFacade", "RemoveProject", value.id); setProjectId(""); setConversationId(""); setMessages([]); await loadProjects(); setNotice("项目已从 SciAide 移除。"); } catch (error) { setNotice(errorText(error)); }
  }

  async function exportProjectArchive(value: Project) {
    if (archiveBusy) return;
    setArchiveBusy("export");
    try {
      const result = await backend<ProjectArchiveExportResult>("ProjectArchiveFacade", "ExportProject", value.id);
      if (!result.path) return;
      setNotice(`项目归档已导出：${fileSize(result.sizeBytes)} · ${result.fileCount} 个文件 · SHA256 ${result.sha256.slice(0, 12)}…`);
    } catch (error) { setNotice(errorText(error)); }
    finally { setArchiveBusy(""); }
  }

  async function restoreProjectArchive() {
    if (archiveBusy) return;
    setArchiveBusy("restore");
    try {
      const result = await backend<ProjectArchiveRestoreReport>("ProjectArchiveFacade", "RestoreProject");
      if (!result.project?.id) return;
      await loadProjects();
      setProjectId(result.project.id); setConversationId(""); setMessages([]); setArchiveReport(result);
    } catch (error) { setNotice(errorText(error)); }
    finally { setArchiveBusy(""); }
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

  async function saveMessageArtifact(message: Message) {
	if (!selectedProject || message.role !== "assistant" || message.status !== "complete") return;
	try {
		const result = await backend<ArtifactSaveResult>("ArtifactFacade", "SaveAssistantAnswer", { projectId: selectedProject.id, messageId: message.id, name: "", artifactId: "" });
		setNotice(result.created ? `已保存为科研产物：${result.artifact.name}` : `该回答已保存：${result.artifact.name}`);
	} catch (error) { setNotice(errorText(error)); }
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

const MessageRow = memo(function MessageRow({ message, providerName, run, runActive, retryStatus, runSteps, toolCalls, approvals, revealing, resolvingApprovalId, resolveApproval, saveArtifact }: {
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
	saveArtifact: (message: Message) => Promise<void>;
}) {
  const attachments = attachmentsOf(message);
  return <article className={`message ${message.role}${revealing ? " revealing" : ""}`} aria-busy={revealing}>
    <div className="avatar">{message.role === "user" ? "你" : <Icon name="spark" size={17}/>}</div>
    <div className="message-body">
      <div className="message-meta"><b>{message.role === "user" ? "你" : providerName}</b>{message.status === "incomplete" && <span>生成已中断</span>}</div>
      {attachments.length > 0 && <div className="message-attachments">{attachments.map((item) => <div className="attachment-card" key={item.attachmentId}><span><Icon name={item.format === "image" ? "model" : "skill"} size={16}/></span><div><b title={item.originalName}>{item.originalName}</b><small>{attachmentSummary(item)}</small></div></div>)}</div>}
      {message.role === "assistant" && run && !runActive && <RunProcess run={run} active={false} retryStatus={null} steps={runSteps} reasoning={message.reasoning} toolCalls={toolCalls} approvals={approvals} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>}
      {message.role === "assistant" && !run && message.runId && <HistoricalRunProcess runId={message.runId} reasoning={message.reasoning} resolveApproval={resolveApproval}/>}
      <CitedAnswer message={message} revealing={revealing} saveArtifact={saveArtifact}/>
      {message.role === "assistant" && run && runActive && <RunProcess run={run} active retryStatus={retryStatus} steps={runSteps} reasoning={message.reasoning} toolCalls={toolCalls} approvals={approvals} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>}
    </div>
  </article>;
});

function CitedAnswer({ message, revealing = false, saveArtifact }: { message: Message; revealing?: boolean; saveArtifact: (message: Message) => Promise<void> }) {
  const [selectedReference, setSelectedReference] = useState("");
  const [showRawQuote, setShowRawQuote] = useState(false);
  const [copied, setCopied] = useState(false);
	const [saving, setSaving] = useState(false);
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
    {message.role === "assistant" && text && !revealing && <div className="message-actions"><button type="button" aria-label="复制回答" title={copied ? "已复制" : "复制回答"} onClick={() => void copyAnswer()}><Icon name={copied ? "check" : "copy"} size={14}/><span>{copied ? "已复制" : "复制"}</span></button>{message.status === "complete" && <button type="button" aria-label="保存为科研产物" title="把完整回答和引用快照保存为不可变科研产物" disabled={saving} onClick={() => { setSaving(true); void saveArtifact(message).finally(() => setSaving(false)); }}><Icon name={saving ? "refresh" : "archive"} size={14}/><span>{saving ? "保存中" : "保存产物"}</span></button>}</div>}
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
  const localExecution = call.toolName === "builtin.shell.execute" || call.toolName === "builtin.python.execute";
  const localExecutionSummary = localExecution ? summarizeLocalExecution(call) : "";
  return <article className={`tool-card ${call.status}`}>
    <header><span className="tool-icon"><Icon name="tool" size={15}/></span><div><b>{call.toolName}</b><small>v{call.toolVersion} · {toolStatusText[call.status] ?? call.status}</small></div><span className={`risk ${call.risk}`}>{call.risk}</span></header>
    <details open={Boolean(approval && localExecution)}><summary>查看参数与资源</summary><pre>{argumentText}</pre>{call.permissions.length > 0 && <div className="permission-list">{call.permissions.map((permission) => <span key={`${permission.kind}:${permission.resource}`}><b>{permission.kind}</b>{permission.resource || "全部资源"}</span>)}</div>}</details>
    {approval && <div className="approval-panel"><div><b>{localExecution ? "确认本机进程执行" : "需要你的确认"}</b>{localExecutionSummary && <code>{localExecutionSummary}</code>}<p>{localExecution ? "命令会在当前项目 Workspace 中运行。这是受审计的本机执行器，不是强安全沙箱；接受前请核对上方完整参数。" : "Plan 模式下，本次工具调用只有在接受后才会执行。风险标签仅供参考，决定权完全属于你。"}</p></div><div className="approval-actions"><button disabled={Boolean(resolvingApprovalId)} onClick={() => void resolveApproval(approval, false)}>拒绝</button><button className="accept" disabled={Boolean(resolvingApprovalId)} onClick={() => void resolveApproval(approval, true)}>{resolvingApprovalId === approval.id ? "处理中…" : "接受"}</button></div></div>}
    {call.result && <div className={`tool-result ${call.result.status}`}><span>{call.result.text || toolStatusText[call.status] || call.status}</span>{call.result.truncated && <small>结果已截断</small>}{call.result.meta?.durationMillis !== undefined && <small>{call.result.meta.durationMillis} ms</small>}</div>}
    {!call.result && call.errorMessage && <div className="tool-result error">{call.errorMessage}</div>}
  </article>;
}

function summarizeLocalExecution(call: ToolCall): string {
  const args = typeof call.arguments === "object" && call.arguments !== null ? call.arguments as Record<string, unknown> : {};
  const runtime = call.toolName === "builtin.python.execute" ? "Python" : String(args.shell || "PowerShell");
  const workdir = String(args.workdir || ".");
  const timeout = typeof args.timeoutSeconds === "number" ? args.timeoutSeconds : 30;
  return `${runtime} · Workspace/${workdir} · ${timeout} 秒`;
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

function ProjectArchiveReport({ report, close, openModels, openSkills }: { report: ProjectArchiveRestoreReport; close: () => void; openModels: () => void; openSkills: () => void }) {
  const stats = [
    ["恢复项目", report.project.name],
    ["归档文件", report.filesRestored.toLocaleString()],
    ["恢复数据", fileSize(report.bytesRestored)],
    ["Skill", `${report.restoredSkillBindings} 已匹配 / ${report.missingSkillBindings.length} 缺失`],
  ];
  return <div className="modal-backdrop compact"><section className="project-archive-report" role="dialog" aria-modal="true" aria-labelledby="project-archive-report-title">
    <header><span><Icon name="archive" size={20}/></span><div><p>PROJECT RESTORE</p><h2 id="project-archive-report-title">项目已安全恢复</h2><small>已创建新的 SciAide 托管项目，原项目和归档文件未被修改。</small></div><button type="button" aria-label="关闭恢复报告" onClick={close}><Icon name="close" size={17}/></button></header>
    <div className="archive-report-summary">{stats.map(([label, value]) => <div key={label}><span>{label}</span><b title={value}>{value}</b></div>)}</div>
    <section className="archive-security-note"><Icon name="shield" size={17}/><div><b>归档不包含任何可复用凭据</b><p>API Key、模型 Header、MCP 配置与 Secret、识图渠道、Embedding 配置、权限授权和临时缓存均未恢复。历史会话保留模型身份，但配置为禁用占位且权限回到 Plan。</p></div></section>
    {report.secretsRequireRebinding && <section className="archive-action-row"><div><b>{report.historicalProfiles} 个历史模型身份需要重新绑定</b><small>历史记录可查看；继续对话前请在“模型与 API”选择或新建可用配置。</small></div><button type="button" onClick={openModels}><Icon name="model" size={14}/>打开模型配置</button></section>}
    {report.missingSkillBindings.length > 0 && <section className="archive-missing-skills"><header><div><b>缺少完全匹配的 Skill 包</b><small>归档只保存版本和哈希绑定，不携带 Skill 源码。</small></div><button type="button" onClick={openSkills}><Icon name="skill" size={14}/>管理 Skills</button></header><div>{report.missingSkillBindings.map((item) => <span key={`${item.skillId}:${item.version}`}><code>${item.skillId}</code><b>v{item.version}</b><small>Content {item.contentHash.slice(0, 10)}… · Package {item.packageHash.slice(0, 10)}…</small></span>)}</div></section>}
    <footer><span><Icon name="check" size={14}/> 项目关系和文件 SHA256 已验证后发布</span><button type="button" onClick={close}>进入恢复项目</button></footer>
  </section></div>;
}

const knowledgeStatusText: Record<KnowledgeDocument["status"], string> = { pending: "等待索引", indexing: "正在索引", ready: "可检索", failed: "索引失败" };
const knowledgeStageText: Record<KnowledgeJob["stage"], string> = { queued: "等待索引", loading: "读取文档", chunking: "分块/向量化", indexing: "提交索引", completed: "可检索", failed: "索引失败", cancelled: "已取消" };
const qualityText: Record<ParseDiagnostic["quality"], string> = { good: "解析正常", warning: "需要核对", poor: "文本不足", unavailable: "解析失败" };
const documentKind = (name: string) => name.includes(".") ? name.split(".").pop()?.toUpperCase() ?? "FILE" : "FILE";
const compactDate = (value: string) => new Date(value).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
const artifactKindText: Record<ArtifactKind, string> = { document: "文档", data: "数据", image: "图片", code: "代码", other: "文件" };
const artifactSourceText: Record<ArtifactVersion["sourceKind"], string> = { assistant_message: "助手回答", workspace_file: "Workspace 文件", tool: "工具产物" };
const artifactCitationStyleText: Record<ArtifactCitationStyle, string> = { gb_t_7714_2015: "GB/T 7714-2015", apa_7: "APA 7" };
const artifactCanExport = (value: ArtifactVersion) => {
  const mimeType = value.mimeType.toLowerCase().split(";", 1)[0]?.trim() ?? "";
  return mimeType.startsWith("text/") || ["application/json", "application/xml", "application/yaml", "application/x-yaml", "application/javascript", "application/pdf"].includes(mimeType) || mimeType.endsWith("+json") || mimeType.endsWith("+xml") || mimeType.includes("wordprocessingml") || mimeType.includes("spreadsheetml");
};
const knowledgeJobActive = (value: KnowledgeDocument) => value.job?.status === "queued" || value.job?.status === "running";
const knowledgeDisplayStatus = (value: KnowledgeDocument) => value.job ? knowledgeStageText[value.job.stage] : knowledgeStatusText[value.status];

const researchReviewText: Record<ResearchReviewStatus, string> = { pending: "待筛选", included: "已纳入", excluded: "已排除" };
const researchImportText: Record<ResearchImportStatus, string> = { not_imported: "未导入", importing: "正在导入", imported: "已进知识库", failed: "导入失败" };
const researchSourceStatusText: Record<ResearchSourceStatus["status"], string> = { ok: "已返回", empty: "无结果", failed: "来源失败" };
const researchAuthorLine = (value: ResearchWork) => value.authors?.map((author) => author.name).filter(Boolean).join("; ") || "作者信息缺失";
const researchIdentifiers = (value: ResearchWork) => [value.identifiers?.doi && `DOI ${value.identifiers.doi}`, value.identifiers?.pmid && `PMID ${value.identifiers.pmid}`, value.identifiers?.arxiv && `arXiv ${value.identifiers.arxiv}`, value.identifiers?.openAlex && `OpenAlex ${value.identifiers.openAlex}`].filter(Boolean) as string[];
const researchBibliographyFieldText: Record<string, string> = { authors: "作者", year: "年份", title: "题名", published: "发布日期", container_title: "期刊 / 会议", volume: "卷", issue: "期", pages: "页码", publisher: "出版社", doi: "DOI", pmid: "PMID", pmcid: "PMCID", arxiv: "arXiv", openalex: "OpenAlex", url: "URL", work_type: "文献类型", language: "语言" };
const researchEvidenceFieldText: Record<ResearchEvidenceField, string> = { research_question: "研究问题", method: "研究方法", sample_dataset: "样本 / 数据集", finding: "主要结论", limitation: "研究局限", note: "用户笔记" };
const researchEvidenceLevelText: Record<ResearchEvidenceLevel, string> = { none: "无证据定位", full_text: "本地全文", metadata_abstract: "元数据 / 摘要，非全文" };
const researchEvidenceReviewText: Record<ResearchEvidenceEntry["reviewStatus"], string> = { pending: "待复核", verified: "已核验", rejected: "已拒绝" };

function bibliographyForm(value: ResearchBibliographyData): ResearchBibliographyData {
  return { ...value, authors: value.authors?.map((author) => ({ ...author })) ?? [] };
}

function BibliographyEvidenceWorkspace({ project, candidate, sourceNames, close, feedback }: { project: Project; candidate: ResearchCandidate; sourceNames: Record<string, string>; close: () => void; feedback: (value: string) => void }) {
  const [bibliography, setBibliography] = useState<ResearchBibliography | null>(null);
  const [draft, setDraft] = useState<ResearchBibliographyData>({ authors: [] });
  const [reason, setReason] = useState("");
  const [evidence, setEvidence] = useState<ResearchEvidenceEntry[]>([]);
  const [evidenceField, setEvidenceField] = useState<ResearchEvidenceField>("finding");
  const [evidenceContent, setEvidenceContent] = useState("");
  const [evidenceProvenance, setEvidenceProvenance] = useState<"user" | "model">("user");
  const [evidenceReview, setEvidenceReview] = useState<"pending" | "verified" | "rejected">("pending");
  const [evidenceQuery, setEvidenceQuery] = useState("");
  const [evidenceMatches, setEvidenceMatches] = useState<ResearchEvidenceSearchMatch[]>([]);
  const [selectedEvidence, setSelectedEvidence] = useState<ResearchEvidenceSearchMatch | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");

  const reload = useCallback(async () => {
    const [nextBibliography, nextEvidence] = await Promise.all([
      backend<ResearchBibliography>("ResearchFacade", "GetBibliography", project.id, candidate.id),
      backend<ResearchEvidenceEntry[]>("ResearchFacade", "ListEvidence", project.id, candidate.id),
    ]);
    setBibliography(nextBibliography); setDraft(bibliographyForm(nextBibliography.data)); setEvidence(nextEvidence);
  }, [candidate.id, project.id]);

  useEffect(() => {
    let active = true; setLoading(true);
    reload().catch((error: unknown) => active && feedback(errorText(error))).finally(() => active && setLoading(false));
    return () => { active = false; };
  }, [feedback, reload]);

  const sourcesByField = useMemo(() => {
    const latest: Record<string, Map<string, ResearchBibliographyFieldSource>> = {};
    for (const value of bibliography?.fieldSources ?? []) {
      const records = (latest[value.field] ??= new Map());
      const key = value.sourceRecordId || `${value.sourceIdSnapshot}:${value.sourceRecordIdSnapshot}`;
      const previous = records.get(key);
      if (!previous || value.observedAt > previous.observedAt || (value.observedAt === previous.observedAt && value.id > previous.id)) records.set(key, value);
    }
    return Object.fromEntries(Object.entries(latest).map(([field, records]) => [field, [...records.values()].sort((left, right) => left.sourceIdSnapshot.localeCompare(right.sourceIdSnapshot) || left.sourceRecordIdSnapshot.localeCompare(right.sourceRecordIdSnapshot))]));
  }, [bibliography?.fieldSources]);

  function changeField(field: keyof ResearchBibliographyData, value: string | number | ResearchBibliographicAuthor[]) {
    setDraft((current) => ({ ...current, [field]: value }));
  }

  function changeAuthors(value: string) {
    const names = value.split(/\r?\n/).map((name) => name.trim()).filter(Boolean);
    setDraft((current) => {
      const remaining = [...(current.authors ?? [])];
      const authors = names.map((name) => {
        const exact = remaining.findIndex((author) => author.name.trim().localeCompare(name, undefined, { sensitivity: "accent" }) === 0);
        const matched = exact >= 0 ? remaining.splice(exact, 1)[0] : undefined;
        return { name, ...(matched?.orcid ? { orcid: matched.orcid } : {}) };
      });
      return { ...current, authors };
    });
  }

  async function saveBibliography() {
    if (!bibliography || busy) return;
    setBusy("bibliography");
    try {
      const updated = await backend<ResearchBibliography>("ResearchFacade", "ReviseBibliography", { projectId: project.id, candidateId: candidate.id, data: draft, reason });
      setBibliography(updated); setDraft(bibliographyForm(updated.data)); setReason(""); feedback("规范书目已保存，修订历史已追加。");
    } catch (error) { feedback(errorText(error)); } finally { setBusy(""); }
  }

  async function selectFieldSource(field: string, sourceRecordId: string) {
    if (!sourceRecordId || busy) return;
    setBusy(`source:${field}`);
    try {
      const updated = await backend<ResearchBibliography>("ResearchFacade", "SelectBibliographySource", { projectId: project.id, candidateId: candidate.id, field, sourceRecordId, reason: "用户选择来源记录" });
      setBibliography(updated); setDraft(bibliographyForm(updated.data)); feedback(`${researchBibliographyFieldText[field] ?? field}已切换到所选来源。`);
    } catch (error) { feedback(errorText(error)); } finally { setBusy(""); }
  }

  async function searchEvidence(event: FormEvent) {
    event.preventDefault(); if (!evidenceQuery.trim() || busy) return;
    setBusy("evidence-search"); setSelectedEvidence(null);
    try {
      const values = await backend<ResearchEvidenceSearchMatch[]>("ResearchFacade", "SearchEvidence", project.id, candidate.id, evidenceQuery.trim());
      setEvidenceMatches(values); if (values.length === 0) feedback("该文献的本地知识 Chunk 中没有命中。请调整检索词。");
    } catch (error) { feedback(errorText(error)); } finally { setBusy(""); }
  }

  async function saveEvidence() {
    if (!evidenceContent.trim() || busy) return;
    if ((evidenceField !== "note" || evidenceProvenance === "model") && !selectedEvidence) { feedback(evidenceProvenance === "model" ? "模型生成的条目必须先绑定该文献的本地证据 Chunk。" : "研究事实必须先绑定该文献的本地证据 Chunk。"); return; }
    setBusy("evidence-save");
    try {
      await backend<ResearchEvidenceEntry>("ResearchFacade", "SaveEvidence", { projectId: project.id, candidateId: candidate.id, field: evidenceField, content: evidenceContent.trim(), provenance: evidenceProvenance, reviewStatus: evidenceReview, reference: selectedEvidence?.reference });
      setEvidenceContent(""); setSelectedEvidence(null); setEvidenceMatches([]); setEvidenceQuery(""); await reload(); feedback("证据矩阵条目已保存并完成本地 Chunk 校验。");
    } catch (error) { feedback(errorText(error)); } finally { setBusy(""); }
  }

  async function deleteEvidence(id: string) {
    if (busy) return; setBusy(`delete:${id}`);
    try { await backend<void>("ResearchFacade", "DeleteEvidence", project.id, candidate.id, id); setEvidence((current) => current.filter((item) => item.id !== id)); feedback("证据矩阵条目已删除。"); }
    catch (error) { feedback(errorText(error)); } finally { setBusy(""); }
  }

  async function reviewEvidence(id: string, reviewStatus: ResearchEvidenceEntry["reviewStatus"]) {
    if (busy) return; setBusy(`review:${id}`);
    try {
      const updated = await backend<ResearchEvidenceEntry>("ResearchFacade", "ReviewEvidence", { projectId: project.id, candidateId: candidate.id, evidenceId: id, reviewStatus });
      setEvidence((current) => current.map((item) => item.id === id ? updated : item));
      feedback(reviewStatus === "verified" ? "证据条目已核验。" : reviewStatus === "rejected" ? "证据条目已拒绝，原始证据快照仍保留。" : "证据条目已重新标记为待复核。");
    } catch (error) { feedback(errorText(error)); } finally { setBusy(""); }
  }

  const fieldInput = (field: keyof ResearchBibliographyData, label: string, wide = false) => <label className={wide ? "wide" : ""}><span>{label}</span><input value={String(draft[field] ?? "")} onChange={(event) => changeField(field, field === "year" ? Math.max(0, Number(event.target.value) || 0) : event.target.value)} /></label>;
  if (loading || !bibliography) return <div className="research-evidence-workspace"><header><button type="button" onClick={close}><Icon name="back" size={14}/>返回候选</button><b>正在读取规范书目…</b></header></div>;
  const primaryEvidenceLevel = bibliography.materials.some((item) => item.evidenceLevel === "full_text") ? "full_text" : bibliography.materials[0]?.evidenceLevel;

  return <div className="research-evidence-workspace">
    <header><button type="button" onClick={close}><Icon name="back" size={14}/>返回候选</button><div><b>{candidate.preferred.title}</b><small>规范书目修订 {bibliography.revision} · {bibliography.materials.length} 个本地材料</small></div><span className={primaryEvidenceLevel ?? "none"}>{primaryEvidenceLevel ? researchEvidenceLevelText[primaryEvidenceLevel] : "尚未导入本地材料"}</span></header>
    <div className="research-evidence-columns">
      <section className="bibliography-editor"><div className="workspace-heading"><div><b>规范书目</b><small>只使用来源记录或用户修订，不猜测缺失字段</small></div><button type="button" disabled={Boolean(busy)} onClick={() => void saveBibliography()}><Icon name="check" size={13}/>保存修订</button></div>
        <div className="bibliography-fields"><label className="wide"><span>作者（每行一位）</span><textarea value={(draft.authors ?? []).map((author) => author.name).join("\n")} onChange={(event) => changeAuthors(event.target.value)}/></label>{fieldInput("year", "年份")}{fieldInput("title", "题名", true)}{fieldInput("containerTitle", "期刊 / 会议", true)}{fieldInput("volume", "卷")}{fieldInput("issue", "期")}{fieldInput("pages", "页码")}{fieldInput("publisher", "出版社")}{fieldInput("doi", "DOI", true)}{fieldInput("pmid", "PMID")}{fieldInput("pmcid", "PMCID")}{fieldInput("arxiv", "arXiv")}{fieldInput("openAlex", "OpenAlex")}{fieldInput("url", "URL", true)}{fieldInput("workType", "文献类型")}{fieldInput("language", "语言")}</div>
        <label className="bibliography-reason"><span>修订依据</span><input value={reason} onChange={(event) => setReason(event.target.value)} maxLength={2000} placeholder="例如：已核对出版社页面"/></label>
        <div className="bibliography-source-grid"><div className="workspace-heading"><div><b>字段来源与冲突</b><small>选择来源会追加修订记录</small></div></div>{Object.entries(sourcesByField).map(([field, values]) => <label key={field}><span>{researchBibliographyFieldText[field] ?? field}<i>{values.length > 1 ? `${values.length} 个来源` : "单一来源"}</i></span><select value={(bibliography.selectedSources[field] ?? "").replace(/^(auto|source):/, "")} onChange={(event) => void selectFieldSource(field, event.target.value)} disabled={Boolean(busy)}>{values.map((value) => <option value={value.sourceRecordId} key={value.id}>{sourceNames[value.sourceIdSnapshot] ?? value.sourceIdSnapshot} · {String(typeof value.value === "object" ? JSON.stringify(value.value) : value.value)}</option>)}</select></label>)}</div>
        <details className="bibliography-history"><summary><Icon name="history" size={13}/>修订历史 · {bibliography.revisions.length}</summary>{bibliography.revisions.length === 0 ? <p>尚无用户修订。</p> : bibliography.revisions.slice().reverse().map((item) => <article key={item.id}><b>修订 {item.revision} · {researchBibliographyFieldText[item.field] ?? item.field}</b><small>{item.sourceKind === "user_edit" ? "用户编辑" : "选择来源"} · {compactDate(item.createdAt)}</small><p>{JSON.stringify(item.previous)} → {JSON.stringify(item.next)}</p>{item.reason && <em>{item.reason}</em>}</article>)}</details>
      </section>
      <section className="evidence-matrix"><div className="workspace-heading"><div><b>证据矩阵</b><small>研究事实必须定位到该文献的本地 Chunk</small></div><span>{evidence.length}</span></div>
        <div className="evidence-list">{evidence.length === 0 ? <div className="research-empty"><Icon name="library" size={22}/><b>尚无证据条目</b><p>先在下方检索本地原文，再保存研究问题、方法、结论或局限。</p></div> : evidence.map((item) => <article className={`review-${item.reviewStatus}`} key={item.id}><header><b>{researchEvidenceFieldText[item.field]}</b><span className={item.evidenceLevel}>{researchEvidenceLevelText[item.evidenceLevel]}</span><button type="button" title="删除条目" onClick={() => void deleteEvidence(item.id)} disabled={Boolean(busy)}><Icon name="trash" size={12}/></button></header><p>{item.content}</p><footer><span className={`evidence-review-status ${item.reviewStatus}`}>{item.provenance === "model" ? `模型生成 · ${researchEvidenceReviewText[item.reviewStatus]}` : `用户记录 · ${researchEvidenceReviewText[item.reviewStatus]}`}</span>{item.evidence && <code>{item.evidence.sourceName} · {item.evidence.locator}</code>}</footer>{item.evidence && <blockquote>{item.evidence.quote}</blockquote>}<div className="evidence-review-actions">{item.reviewStatus !== "verified" && <button type="button" onClick={() => void reviewEvidence(item.id, "verified")} disabled={Boolean(busy)}><Icon name="check" size={11}/>核验</button>}{item.reviewStatus !== "rejected" && <button type="button" className="reject" onClick={() => void reviewEvidence(item.id, "rejected")} disabled={Boolean(busy)}><Icon name="close" size={11}/>拒绝</button>}{item.reviewStatus !== "pending" && <button type="button" onClick={() => void reviewEvidence(item.id, "pending")} disabled={Boolean(busy)}>重新待复核</button>}</div></article>)}</div>
        <form className="evidence-search" onSubmit={searchEvidence}><div><Icon name="search" size={13}/><input value={evidenceQuery} onChange={(event) => setEvidenceQuery(event.target.value)} maxLength={200} placeholder="检索该文献的本地证据"/></div><button type="submit" disabled={Boolean(busy) || !evidenceQuery.trim()}>检索</button></form>
        {evidenceMatches.length > 0 && <div className="evidence-match-list">{evidenceMatches.map((item) => <button type="button" className={selectedEvidence?.reference.chunkId === item.reference.chunkId ? "selected" : ""} key={item.reference.chunkId} onClick={() => setSelectedEvidence(item)}><span>{item.sourceName} · {item.locator}</span><p>{item.snippet}</p></button>)}</div>}
        <div className="evidence-entry-editor"><div><select value={evidenceField} onChange={(event) => setEvidenceField(event.target.value as ResearchEvidenceField)}>{Object.entries(researchEvidenceFieldText).map(([value, label]) => <option value={value} key={value}>{label}</option>)}</select><select value={evidenceProvenance} onChange={(event) => { const value = event.target.value as "user" | "model"; setEvidenceProvenance(value); if (value === "model") setEvidenceReview("pending"); }}><option value="user">用户记录</option><option value="model">模型生成</option></select><select value={evidenceReview} disabled={evidenceProvenance === "model"} onChange={(event) => setEvidenceReview(event.target.value as "pending" | "verified" | "rejected")}><option value="pending">待复核</option><option value="verified">已核验</option><option value="rejected">已拒绝</option></select></div><textarea value={evidenceContent} onChange={(event) => setEvidenceContent(event.target.value)} maxLength={20000} placeholder="填写要纳入矩阵的研究事实或笔记"/><button type="button" onClick={() => void saveEvidence()} disabled={Boolean(busy) || !evidenceContent.trim() || (!selectedEvidence && (evidenceField !== "note" || evidenceProvenance === "model"))}><Icon name="plus" size={13}/>加入证据矩阵</button>{selectedEvidence && <p>已绑定：{selectedEvidence.sourceName} · {selectedEvidence.locator}</p>}</div>
      </section>
    </div>
  </div>;
}

function ResearchDiscovery({ project, close, openKnowledge }: { project: Project; close: () => void; openKnowledge: () => void }) {
  const [sources, setSources] = useState<ResearchSource[]>([]);
  const [selectedSources, setSelectedSources] = useState<string[]>([]);
  const [queries, setQueries] = useState<ResearchQuery[]>([]);
  const [queryId, setQueryId] = useState("");
  const [queryText, setQueryText] = useState("");
  const [page, setPage] = useState<ResearchCandidatePage>({ items: [], total: 0, offset: 0, limit: 20 });
  const [candidateId, setCandidateId] = useState("");
  const [statusFilter, setStatusFilter] = useState<"" | ResearchReviewStatus>("");
  const [filterText, setFilterText] = useState("");
  const [sort, setSort] = useState("relevance");
  const [exclusionReason, setExclusionReason] = useState("");
  const [note, setNote] = useState("");
  const [loading, setLoading] = useState(true);
  const [searching, setSearching] = useState(false);
  const [busyAction, setBusyAction] = useState("");
  const [feedback, setFeedback] = useState("");
  const [evidenceCandidateId, setEvidenceCandidateId] = useState("");
  const candidateRequests = useRef(createLatestRequestGate());

  const loadQueries = useCallback(async (preferred = "") => {
    const values = await backend<ResearchQuery[]>("ResearchFacade", "ListQueries", project.id);
    setQueries(values);
    setQueryId((current) => values.some((item) => item.id === (preferred || current)) ? (preferred || current) : first(values)?.id ?? "");
  }, [project.id]);

  const loadCandidates = useCallback(async (offset = 0) => {
    const request = candidateRequests.current.begin();
    try {
      const values = await backend<ResearchCandidatePage>("ResearchFacade", "ListCandidates", { projectId: project.id, queryId, status: statusFilter, search: filterText, sort, offset, limit: 20 });
      if (!candidateRequests.current.isCurrent(request)) return;
      setPage(values);
      setCandidateId((current) => values.items.some((item) => item.id === current) ? current : first(values.items)?.id ?? "");
    } catch (error) {
      if (candidateRequests.current.isCurrent(request)) throw error;
    }
  }, [filterText, project.id, queryId, sort, statusFilter]);

  useEffect(() => {
    let active = true;
    setLoading(true);
    Promise.all([backend<ResearchSource[]>("ResearchFacade", "Catalog"), backend<ResearchQuery[]>("ResearchFacade", "ListQueries", project.id)]).then(([catalog, history]) => {
      if (!active) return;
      setSources(catalog); setSelectedSources(catalog.map((item) => item.id)); setQueries(history); setQueryId(first(history)?.id ?? "");
    }).catch((error: unknown) => active && setFeedback(errorText(error))).finally(() => active && setLoading(false));
    return () => { active = false; };
  }, [project.id]);
  useEffect(() => {
    if (!loading) void loadCandidates().catch((error: unknown) => setFeedback(errorText(error)));
    return () => { candidateRequests.current.invalidate(); };
  }, [loadCandidates, loading]);

  const selected = page.items.find((item) => item.id === candidateId);
  const evidenceCandidate = page.items.find((item) => item.id === evidenceCandidateId);
  const activeQuery = queries.find((item) => item.id === queryId);
  useEffect(() => { setExclusionReason(selected?.exclusionReason ?? ""); setNote(selected?.note ?? ""); }, [selected?.id, selected?.exclusionReason, selected?.note]);

  async function runSearch(event: FormEvent) {
    event.preventDefault();
    if (!queryText.trim() || selectedSources.length === 0 || searching) return;
    setSearching(true); setFeedback("");
    try {
      const result = await backend<ResearchSearchResult>("ResearchFacade", "Search", { projectId: project.id, query: queryText.trim(), sourceIds: selectedSources, limit: 20 });
      candidateRequests.current.invalidate();
      setQueries((current) => [result.query, ...current.filter((item) => item.id !== result.query.id)]);
      setQueryId(result.query.id); setPage(result.page); setCandidateId(first(result.page.items)?.id ?? "");
      const failures = result.query.sources.filter((item) => item.status === "failed");
      setFeedback(failures.length ? `检索已保存，${failures.length} 个来源失败，其余结果仍可筛选。` : `已保存 ${result.query.resultCount} 条来源记录。`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setSearching(false); }
  }

  async function updateReview(status: ResearchReviewStatus) {
    if (!selected || busyAction) return;
    if (status === "excluded" && !exclusionReason.trim()) { setFeedback("排除候选时需要填写排除原因。"); return; }
    setBusyAction(`review:${status}`); setFeedback("");
    try {
      const updated = await backend<ResearchCandidate>("ResearchFacade", "UpdateReview", { projectId: project.id, candidateId: selected.id, status, exclusionReason, note });
      setPage((current) => ({ ...current, items: current.items.map((item) => item.id === updated.id ? updated : item) }));
      setFeedback(status === "included" ? "候选已纳入。现在可以显式加入知识库。" : status === "excluded" ? "候选已排除，原因已保存。" : "候选已恢复为待筛选。");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function saveNote() {
    if (!selected || busyAction) return;
    setBusyAction("note");
    try {
      const updated = await backend<ResearchCandidate>("ResearchFacade", "UpdateReview", { projectId: project.id, candidateId: selected.id, status: selected.reviewStatus, exclusionReason, note });
      setPage((current) => ({ ...current, items: current.items.map((item) => item.id === updated.id ? updated : item) }));
      setFeedback("筛选笔记已保存。");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function importCandidate(mode: "auto" | "full_text" | "metadata_abstract") {
    if (!selected || selected.reviewStatus !== "included" || busyAction) return;
    setBusyAction(`import:${mode}`); setFeedback("");
    try {
      const result = await backend<ResearchImportResult>("ResearchFacade", "ImportCandidate", { projectId: project.id, candidateId: selected.id, mode });
      setPage((current) => ({ ...current, items: current.items.map((item) => item.id === result.candidate.id ? result.candidate : item) }));
      setFeedback(result.candidate.importKind === "full_text" ? "开放全文已导入，知识索引正在建立。" : "元数据/摘要已导入并明确标注为非全文，知识索引正在建立。");
    } catch (error) {
      setFeedback(errorText(error));
      await loadCandidates(page.offset).catch(() => undefined);
    } finally { setBusyAction(""); }
  }

  function toggleSource(sourceId: string) {
    setSelectedSources((current) => current.includes(sourceId) ? current.filter((item) => item !== sourceId) : [...current, sourceId]);
  }

  return <div className="modal-backdrop"><section className="research-modal" role="dialog" aria-modal="true">
    <header><div><span className="dialog-icon research"><Icon name="search" size={19}/></span><div><p>LITERATURE DISCOVERY</p><h2>{project.name} · 文献发现</h2></div></div><button type="button" className="close" onClick={close}><Icon name="close"/></button></header>
    <form className="research-searchbar" onSubmit={runSearch}><div><Icon name="search" size={16}/><input value={queryText} onChange={(event) => setQueryText(event.target.value)} placeholder="输入题名、作者、DOI、主题词或布尔检索式" maxLength={500}/></div><button type="submit" disabled={searching || !queryText.trim() || selectedSources.length === 0}><Icon name={searching ? "refresh" : "search"} size={15}/>{searching ? "正在检索" : "检索并保存"}</button></form>
    <div className="research-source-picker">{sources.map((source) => <button type="button" className={selectedSources.includes(source.id) ? "selected" : ""} onClick={() => toggleSource(source.id)} key={source.id} title={source.description}><span className="research-source-check">{selectedSources.includes(source.id) && <Icon name="check" size={11}/>}</span><b>{source.name}</b><small>{source.fullText ? "开放全文" : "元数据"}</small></button>)}</div>
    <div className="research-boundary"><Icon name="shield" size={15}/><span>在线命中是不可信候选，不会直接成为 `[K-...]` 引用。只有显式纳入并进入本地知识库后，原文 Chunk 才能产生可信引用。</span></div>
    {evidenceCandidate ? <BibliographyEvidenceWorkspace project={project} candidate={evidenceCandidate} sourceNames={Object.fromEntries(sources.map((item) => [item.id, item.name]))} close={() => setEvidenceCandidateId("")} feedback={setFeedback}/> : <div className="research-layout">
      <aside className="research-query-panel"><div className="research-panel-heading"><b>已保存检索</b><span>{queries.length}</span></div><div className="research-query-list">{loading ? <p>正在读取…</p> : queries.length === 0 ? <p>完成一次检索后，查询和来源状态会保存在当前项目。</p> : queries.map((item) => <button type="button" className={queryId === item.id ? "selected" : ""} key={item.id} onClick={() => setQueryId(item.id)}><b>{item.text}</b><small>{item.resultCount} 条来源记录 · {compactDate(item.updatedAt)}</small><span className={item.partial ? "partial" : "complete"}>{item.partial ? "部分结果" : "完整"}</span></button>)}</div>{activeQuery && <div className="research-source-status"><b>本次来源</b>{activeQuery.sources.map((item) => <div className={item.status} key={item.sourceId}><i/><span><strong>{sources.find((source) => source.id === item.sourceId)?.name ?? item.sourceId}</strong><small>{item.message || `${researchSourceStatusText[item.status]} · ${item.count} 条`}</small></span></div>)}</div>}</aside>
      <section className="research-results-panel"><div className="research-results-toolbar"><div><Icon name="search" size={14}/><input value={filterText} onChange={(event) => setFilterText(event.target.value)} placeholder="筛选当前候选"/></div><select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value as "" | ResearchReviewStatus)}><option value="">全部状态</option><option value="pending">待筛选</option><option value="included">已纳入</option><option value="excluded">已排除</option></select><select value={sort} onChange={(event) => setSort(event.target.value)}><option value="relevance">相关度</option><option value="year_desc">年份从新到旧</option><option value="cited_desc">被引量</option><option value="title">题名字母序</option><option value="updated">最近更新</option></select></div><div className="research-result-count">{page.total} 个聚合候选 · 当前显示 {page.items.length}</div><div className="research-candidate-list">{page.items.length === 0 ? <div className="research-empty"><Icon name="search" size={25}/><b>{queryId ? "当前筛选没有候选" : "先执行或选择一个检索"}</b><p>来源失败与真正无结果会分别显示，不会混为零结果。</p></div> : page.items.map((item) => <button type="button" className={`${candidateId === item.id ? "selected" : ""} ${item.reviewStatus}`} key={item.id} onClick={() => setCandidateId(item.id)}><div><span className={`research-review-dot ${item.reviewStatus}`}/><b>{item.preferred.title}</b></div><p>{researchAuthorLine(item.preferred)}</p><footer><span>{item.preferred.year || "年份缺失"}{item.preferred.venue ? ` · ${item.preferred.venue}` : ""}</span><span>{item.records.length} 个来源</span><i className={item.importStatus}>{researchImportText[item.importStatus]}</i></footer></button>)}</div>{page.total > page.limit && <div className="research-pagination"><span>{page.offset + 1}-{Math.min(page.offset + page.items.length, page.total)} / {page.total}</span><button type="button" disabled={page.offset === 0} onClick={() => void loadCandidates(Math.max(0, page.offset - page.limit))}>上一页</button><button type="button" disabled={page.offset + page.limit >= page.total} onClick={() => void loadCandidates(page.offset + page.limit)}>下一页</button></div>}</section>
      <main className="research-detail-panel">{!selected ? <div className="research-empty detail"><Icon name="library" size={26}/><b>选择一个候选</b><p>核对聚合来源、摘要和标识符后决定纳入或排除。</p></div> : <><section className="research-detail-title"><div><span className={`research-review-dot ${selected.reviewStatus}`}/><div><h3>{selected.preferred.title}</h3><p>{researchAuthorLine(selected.preferred)}</p></div></div><span className={selected.reviewStatus}>{researchReviewText[selected.reviewStatus]}</span></section><button type="button" className="research-bibliography-open" onClick={() => setEvidenceCandidateId(selected.id)}><Icon name="library" size={14}/><span><b>规范书目与证据矩阵</b><small>核对字段来源、修订书目并定位本地证据</small></span><Icon name="back" size={13}/></button><div className="research-work-meta"><span>{selected.preferred.year || "年份缺失"}</span>{selected.preferred.venue && <span>{selected.preferred.venue}</span>}{selected.preferred.citedByCount !== undefined && <span>被引 {selected.preferred.citedByCount}</span>}{selected.preferred.openAccess && <span className="open">有开放全文线索</span>}</div>{researchIdentifiers(selected.preferred).length > 0 && <div className="research-identifier-list">{researchIdentifiers(selected.preferred).map((value) => <code key={value}>{value}</code>)}</div>}<section className="research-abstract"><b>来源摘要</b><p>{selected.preferred.abstract || "当前来源没有提供摘要。"}</p></section><details className="research-records"><summary>{selected.records.length} 条来源记录与字段冲突</summary>{selected.records.map((record) => <article key={record.id}><header><b>{sources.find((source) => source.id === record.work.sourceId)?.name ?? record.work.sourceId}</b><code>{record.work.sourceRecordId}</code></header><p>{record.work.title}</p><small>{[record.work.year, record.work.venue, record.work.identifiers?.doi, record.work.identifiers?.pmid].filter(Boolean).join(" · ") || "无补充书目信息"}</small></article>)}</details><section className="research-review-editor"><label>筛选笔记<textarea value={note} onChange={(event) => setNote(event.target.value)} maxLength={20000} placeholder="记录纳入标准、质量判断或后续核对事项"/></label><label className={selected.reviewStatus === "excluded" ? "required" : ""}>排除原因<textarea value={exclusionReason} onChange={(event) => setExclusionReason(event.target.value)} maxLength={2000} placeholder="排除时必填，例如：研究对象不符合范围"/></label><div><button type="button" disabled={Boolean(busyAction)} onClick={() => void updateReview("pending")}>待定</button><button type="button" className="exclude" disabled={Boolean(busyAction)} onClick={() => void updateReview("excluded")}><Icon name="close" size={13}/>排除</button><button type="button" className="include" disabled={Boolean(busyAction)} onClick={() => void updateReview("included")}><Icon name="check" size={13}/>纳入</button><button type="button" disabled={Boolean(busyAction)} onClick={() => void saveNote()}>保存笔记</button></div></section><section className={`research-import-panel ${selected.importStatus}`}><header><div><b>加入项目知识库</b><small>{selected.importStatus === "imported" ? (selected.importKind === "full_text" ? "已导入开放全文" : "已导入元数据/摘要，非全文") : researchImportText[selected.importStatus]}</small></div>{selected.importStatus === "imported" && <button type="button" onClick={openKnowledge}><Icon name="library" size={13}/>查看知识库</button>}</header>{selected.importError && <p className="error">{selected.importError}</p>}{selected.importStatus !== "imported" && <div><button type="button" disabled={selected.reviewStatus !== "included" || Boolean(busyAction)} onClick={() => void importCandidate("auto")}><Icon name="download" size={13}/>自动选择</button><button type="button" disabled={selected.reviewStatus !== "included" || Boolean(busyAction)} onClick={() => void importCandidate("full_text")}>仅开放全文</button><button type="button" disabled={selected.reviewStatus !== "included" || Boolean(busyAction)} onClick={() => void importCandidate("metadata_abstract")}>仅元数据/摘要</button></div>}<p>“自动选择”只下载固定科研来源上的开放 PDF；否则生成明确注明非全文的 Markdown。导入后仍需等待本地索引完成。</p></section></>}</main>
    </div>}{feedback && <div className="research-feedback"><Icon name="check" size={14}/><span>{feedback}</span><button type="button" onClick={() => setFeedback("")}><Icon name="close" size={13}/></button></div>}
  </section></div>;
}

function ArtifactDocumentPreview({ document }: { document: ArtifactStructuredPreview }) {
  return <div className="artifact-structured-preview">
    {document.blocks.map((block, index) => {
      if (block.kind === "heading") {
        const level = Math.min(3, Math.max(1, block.level ?? 1));
        const Heading = `h${level + 2}` as "h3" | "h4" | "h5";
        return <Heading key={index}>{block.text}</Heading>;
      }
      if (block.kind === "paragraph") return <p key={index}>{block.text}</p>;
      if (block.kind === "list_item") return <div className="artifact-preview-list" key={index}><span>•</span><p>{block.text}</p></div>;
      if (block.kind === "quote") return <blockquote key={index}>{block.text}</blockquote>;
      if (block.kind === "code") return <pre key={index}>{block.text}</pre>;
      if (block.kind === "rule") return <hr key={index}/>;
      if (block.kind === "table") return <div className="artifact-preview-table-wrap" key={index}><table><tbody>{(block.rows ?? []).map((row, rowIndex) => <tr key={rowIndex}>{row.map((cell, columnIndex) => rowIndex === 0 ? <th key={columnIndex}>{cell}</th> : <td key={columnIndex}>{cell}</td>)}</tr>)}</tbody></table></div>;
      return null;
    })}
  </div>;
}

function ArtifactLibrary({ project, close }: { project: Project; close: () => void }) {
  const [artifacts, setArtifacts] = useState<ResearchArtifact[]>([]);
  const [includeTrashed, setIncludeTrashed] = useState(false);
  const [selectedId, setSelectedId] = useState("");
  const [detail, setDetail] = useState<ArtifactDetail | null>(null);
  const [detailRevision, setDetailRevision] = useState(0);
  const [versionId, setVersionId] = useState("");
  const [preview, setPreview] = useState<ArtifactPreview | null>(null);
  const [integrity, setIntegrity] = useState<ArtifactIntegrity | null>(null);
  const [loading, setLoading] = useState(true);
  const [busyAction, setBusyAction] = useState("");
  const [feedback, setFeedback] = useState("");
  const [exportFormat, setExportFormat] = useState<ArtifactExportFormat>("docx");
  const [citationStyle, setCitationStyle] = useState<ArtifactCitationStyle>("gb_t_7714_2015");

  const load = useCallback(async (preferred = "") => {
    setLoading(true);
    try {
      const values = await backend<ResearchArtifact[]>("ArtifactFacade", "ListArtifacts", project.id, includeTrashed);
      setArtifacts(values);
      setSelectedId((current) => values.some((item) => item.id === (preferred || current)) ? (preferred || current) : first(values)?.id ?? "");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setLoading(false); }
  }, [includeTrashed, project.id]);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    setDetail(null); setPreview(null); setIntegrity(null);
    if (!selectedId) { setDetail(null); setVersionId(""); return; }
    let active = true;
    void backend<ArtifactDetail>("ArtifactFacade", "GetArtifact", project.id, selectedId).then((value) => {
      if (!active) return;
      setDetail(value);
      setVersionId((current) => value.versions.some((item) => item.id === current) ? current : value.artifact.currentVersionId || first(value.versions)?.id || "");
    }).catch((error: unknown) => active && setFeedback(errorText(error)));
    return () => { active = false; };
  }, [detailRevision, project.id, selectedId]);
  useEffect(() => {
    setIntegrity(null); setPreview(null);
    if (!versionId) return;
    let active = true;
    void backend<ArtifactPreview>("ArtifactFacade", "PreviewArtifactVersion", project.id, versionId).then((value) => active && setPreview(value)).catch((error: unknown) => {
      if (active) setPreview({ versionId, kind: "binary", mimeType: "application/octet-stream", truncated: false });
      if (active && !String(errorText(error)).includes("inline preview")) setFeedback(errorText(error));
    });
    return () => { active = false; };
  }, [project.id, versionId]);

  const selected = detail?.artifact;
  const version = detail?.versions.find((item) => item.id === versionId) ?? first(detail?.versions ?? []);
  const activeCount = artifacts.filter((item) => item.status === "active").length;
  const trashedCount = artifacts.filter((item) => item.status === "trashed").length;

  async function registerFile(artifactId = "") {
    if (busyAction) return;
    setBusyAction(artifactId ? "version" : "add"); setFeedback("");
    try {
      const result = await backend<ArtifactSaveResult>("ArtifactFacade", "ChooseAndRegisterWorkspaceFile", project.id, artifactId);
      if (!result.artifact.id) return;
      await load(result.artifact.id);
      setDetailRevision((value) => value + 1);
      setFeedback(result.created ? `已登记科研产物：${result.artifact.name}` : `已保存 v${result.version.versionNumber}：${result.artifact.name}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function renameArtifact() {
    if (!selected || busyAction) return;
    const name = window.prompt("科研产物名称", selected.name)?.trim();
    if (!name || name === selected.name) return;
    setBusyAction("rename");
    try { await backend("ArtifactFacade", "RenameArtifact", project.id, selected.id, name); await load(selected.id); setDetailRevision((value) => value + 1); setFeedback("产物名称已更新，历史版本内容未改变。"); }
    catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function changeStatus() {
    if (!selected || busyAction) return;
    const trashing = selected.status === "active";
    if (trashing && !window.confirm(`将“${selected.name}”移入回收站？\n\n所有不可变版本仍会保留，可随时恢复。`)) return;
    setBusyAction("status");
    try {
      await backend("ArtifactFacade", trashing ? "TrashArtifact" : "RestoreArtifact", project.id, selected.id);
      await load(selected.id); setDetailRevision((value) => value + 1); setFeedback(trashing ? "已移入回收站。" : "科研产物已恢复。");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function checkIntegrity() {
    if (!version || busyAction) return;
    setBusyAction("integrity");
    try { setIntegrity(await backend<ArtifactIntegrity>("ArtifactFacade", "CheckArtifactIntegrity", project.id, version.id)); }
    catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function downloadVersion() {
    if (!version || busyAction) return;
    setBusyAction("download");
    try {
      const path = await backend<string>("ArtifactFacade", "DownloadArtifactVersion", project.id, version.id, version.fileName);
      if (path) setFeedback(`已下载到：${path}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function createExport() {
    if (!version || busyAction) return;
    setBusyAction("export"); setFeedback("");
    try {
      const result = await backend<ArtifactExportResult>("ArtifactFacade", "CreateArtifactExport", { projectId: project.id, versionId: version.id, format: exportFormat, citationStyle });
      setDetailRevision((value) => value + 1);
      setFeedback(result.created ? `已生成 ${result.export.format.toUpperCase()} 导出：${result.export.fileName}` : `相同来源和选项已存在，已复用：${result.export.fileName}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function downloadExport(value: ArtifactExport) {
    if (busyAction) return;
    setBusyAction(`export-download:${value.id}`);
    try {
      const path = await backend<string>("ArtifactFacade", "DownloadArtifactExport", project.id, value.id, value.fileName);
      if (path) setFeedback(`已下载到：${path}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  return <div className="modal-backdrop"><section className="model-modal artifact-modal" role="dialog" aria-modal="true">
    <header><div><span className="dialog-icon"><Icon name="archive" size={19}/></span><div><p>RESEARCH ARTIFACTS</p><h2>{project.name} · 科研产物</h2></div></div><div className="artifact-header-actions"><button type="button" className="artifact-add" disabled={Boolean(busyAction)} onClick={() => void registerFile()}><Icon name={busyAction === "add" ? "refresh" : "plus"} size={15}/>{busyAction === "add" ? "正在登记" : "登记 Workspace 文件"}</button><button type="button" className="close" onClick={close}><Icon name="close"/></button></div></header>
    <div className="artifact-layout">
      <aside className="artifact-list-panel">
        <div className="artifact-list-summary"><span><b>{activeCount}</b> 个产物</span><label><input type="checkbox" checked={includeTrashed} onChange={(event) => setIncludeTrashed(event.target.checked)}/>显示回收站</label></div>
        <div className="artifact-list">{loading ? <div className="artifact-empty"><Icon name="refresh" size={22}/><span>正在读取科研产物</span></div> : artifacts.length === 0 ? <div className="artifact-empty"><span><Icon name="archive" size={25}/></span><b>还没有科研产物</b><p>可从完整助手回答保存，或显式登记 Workspace 中的文件。</p></div> : artifacts.map((item) => <button type="button" className={`${selectedId === item.id ? "selected" : ""} ${item.status}`} key={item.id} onClick={() => setSelectedId(item.id)}><span className="artifact-kind"><Icon name={item.kind === "image" ? "model" : item.kind === "data" ? "chart" : item.kind === "code" ? "tool" : "archive"} size={16}/></span><span><b title={item.name}>{item.name}</b><small>{artifactKindText[item.kind]} · {item.currentVersion ? `v${item.currentVersion.versionNumber}` : "无版本"} · {compactDate(item.updatedAt)}</small></span>{item.status === "trashed" && <i>回收站</i>}</button>)}</div>
        {includeTrashed && trashedCount > 0 && <small className="artifact-trash-count">回收站中有 {trashedCount} 个产物</small>}
      </aside>
      <main className="artifact-detail-panel">{!selected || !version ? <div className="artifact-empty detail"><Icon name="archive" size={27}/><b>选择一个科研产物</b><p>查看当前版本、来源快照、引用与完整性状态。</p></div> : <>
        <section className="artifact-title"><div><span className="artifact-kind large"><Icon name={selected.kind === "image" ? "model" : selected.kind === "data" ? "chart" : selected.kind === "code" ? "tool" : "archive"} size={18}/></span><div><h3>{selected.name}</h3><p>{artifactKindText[selected.kind]} · {version.fileName} · {fileSize(version.sizeBytes)}</p></div></div><div><button type="button" title="重命名" disabled={Boolean(busyAction)} onClick={() => void renameArtifact()}><Icon name="settings" size={14}/></button><button type="button" title={selected.status === "active" ? "移入回收站" : "恢复"} disabled={Boolean(busyAction)} onClick={() => void changeStatus()}><Icon name={selected.status === "active" ? "trash" : "refresh"} size={14}/></button></div></section>
        <div className="artifact-version-toolbar"><label>版本<select value={version.id} onChange={(event) => setVersionId(event.target.value)}>{detail.versions.map((item) => <option value={item.id} key={item.id}>v{item.versionNumber} · {compactDate(item.createdAt)} · {artifactSourceText[item.sourceKind]}</option>)}</select></label><button type="button" disabled={Boolean(busyAction)} onClick={() => void checkIntegrity()}><Icon name={busyAction === "integrity" ? "refresh" : "shield"} size={14}/>完整性</button><button type="button" disabled={Boolean(busyAction)} onClick={() => void downloadVersion()}><Icon name={busyAction === "download" ? "refresh" : "download"} size={14}/>下载</button>{selected.status === "active" && <button type="button" disabled={Boolean(busyAction)} title="从 Workspace 文件创建不可变新版本" onClick={() => void registerFile(selected.id)}><Icon name={busyAction === "version" ? "refresh" : "plus"} size={14}/>新版本</button>}</div>
        {integrity && <div className={`artifact-integrity ${integrity.status}`}><Icon name={integrity.status === "verified" ? "check" : "shield"} size={15}/><div><b>{integrity.status === "verified" ? "对象完整" : integrity.status === "missing" ? "对象缺失" : "校验不一致"}</b><small>{integrity.status === "verified" ? `SHA256 ${integrity.expectedSha256.slice(0, 16)}… · ${fileSize(integrity.actualSize)}` : integrity.message}</small></div></div>}
        <section className="artifact-export-panel"><header><div><b>正式文档导出</b><small>派生文件不会覆盖或推进原始版本</small></div>{version.exports.length > 0 && <span>{version.exports.length} 份历史导出</span>}</header>{artifactCanExport(version) ? <div className="artifact-export-controls"><label>格式<select value={exportFormat} onChange={(event) => setExportFormat(event.target.value as ArtifactExportFormat)}><option value="docx">DOCX</option><option value="pdf">PDF</option></select></label><label>引用样式<select value={citationStyle} onChange={(event) => setCitationStyle(event.target.value as ArtifactCitationStyle)}><option value="gb_t_7714_2015">GB/T 7714-2015</option><option value="apa_7">APA 7</option></select></label><button type="button" disabled={Boolean(busyAction)} onClick={() => void createExport()}><Icon name={busyAction === "export" ? "refresh" : "download"} size={14}/>{busyAction === "export" ? "正在生成" : "生成导出"}</button></div> : <div className="artifact-export-unavailable">图片或不可提取的二进制文件不能转换为研究文档。</div>}{version.exports.length > 0 && <div className="artifact-export-list">{version.exports.map((item) => <div key={item.id}><span className="artifact-export-format">{item.format.toUpperCase()}</span><div><b title={item.fileName}>{item.fileName}</b><small>{artifactCitationStyleText[item.citationStyle]} · {fileSize(item.sizeBytes)} · {compactDate(item.createdAt)}</small></div><button type="button" title="下载此历史导出" aria-label={`下载 ${item.fileName}`} disabled={Boolean(busyAction)} onClick={() => void downloadExport(item)}><Icon name={busyAction === `export-download:${item.id}` ? "refresh" : "download"} size={14}/></button></div>)}</div>}</section>
        <section className="artifact-preview"><header><b>内容预览</b>{preview?.truncated && <span>预览已按结构或长度截断</span>}</header>{!preview ? <div className="artifact-preview-loading">正在读取版本…</div> : preview.kind === "text" ? <pre>{preview.text}</pre> : preview.kind === "document" && preview.document ? <ArtifactDocumentPreview document={preview.document}/> : preview.kind === "image" ? <img alt={selected.name} src={`data:${preview.mimeType};base64,${preview.data}`}/> : <div className="artifact-binary"><Icon name="archive" size={26}/><b>此格式暂不提供内嵌预览</b><span>{version.mimeType} · {fileSize(version.sizeBytes)}</span></div>}</section>
        <div className="artifact-evidence-grid"><section><header><b>来源快照</b><span>{artifactSourceText[version.sourceKind]}</span></header><dl><div><dt>模型</dt><dd>{version.provenance.modelId || "非模型产物"}{version.provenance.modelProfileName ? ` · ${version.provenance.modelProfileName}` : ""}</dd></div><div><dt>来源</dt><dd>{version.provenance.toolName || version.provenance.conversationTitle || version.provenance.workspaceRelativePath || version.lineage[0]?.sourceIdSnapshot}</dd></div><div><dt>Skills</dt><dd>{version.provenance.skills?.length ? version.provenance.skills.map((item) => item.dynamic ? `${item.id} · 动态/${item.origin || "unknown"}` : `${item.id}@${item.version}`).join("、") : "本轮未加载 Skill"}</dd></div><div><dt>SHA256</dt><dd><code title={version.sha256}>{version.sha256.slice(0, 20)}…</code></dd></div></dl></section><section><header><b>可信引用</b><span>{version.citations.length} 条</span></header>{version.citations.length ? <div className="artifact-citations">{version.citations.map((item) => <div key={item.id}><b>{item.sourceName}</b><small>{item.locator || item.title || item.reference}</small><p>{item.quote}</p></div>)}</div> : <div className="artifact-no-citations">此版本没有采用结构化引用。</div>}</section></div>
      </>}</main>
    </div>
    {feedback && <div className="artifact-feedback"><span>{feedback}</span><button type="button" onClick={() => setFeedback("")}><Icon name="close" size={13}/></button></div>}
  </section></div>;
}

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

const shortHash = (value?: string) => value ? value.slice(0, 12) : "未记录";

function PythonEnvironmentSettings({ project, close, feedback }: { project: Project; close: () => void; feedback: (value: string) => void }) {
  const [environment, setEnvironment] = useState<PythonEnvironment | null>(null);
  const [discovery, setDiscovery] = useState<PythonDiscovery | null>(null);
  const [basePath, setBasePath] = useState("");
  const [externalPath, setExternalPath] = useState("");
  const [loading, setLoading] = useState(true);
  const [action, setAction] = useState("");
  const [message, setMessage] = useState("");

  const load = useCallback(async () => {
    setLoading(true); setMessage("");
    try {
      const [current, detected] = await Promise.all([
        backend<PythonEnvironment>("PythonFacade", "GetProjectEnvironment", project.id),
        backend<PythonDiscovery>("PythonFacade", "DetectInterpreters"),
      ]);
      const interpreters = Array.isArray(detected?.interpreters) ? detected.interpreters : [];
      setEnvironment(current); setDiscovery({ ...detected, interpreters });
      const bases = interpreters.filter((item) => item.hasVenv && item.prefix.toLocaleLowerCase() === item.basePrefix.toLocaleLowerCase());
      const preferred = bases.find((item) => item.executablePath === current.baseExecutablePath) ?? bases[0];
      setBasePath(preferred?.executablePath ?? "");
    } catch (error) { setMessage(errorText(error)); }
    finally { setLoading(false); }
  }, [project.id]);

  useEffect(() => { void load(); }, [load]);

  async function chooseBaseInterpreter() {
    if (action) return;
    setAction("choose-base"); setMessage("");
    try {
      const detected = await backend<PythonDiscovery>("PythonFacade", "ChooseBaseInterpreter");
      if (detected?.status === "cancelled") return;
      const selected = Array.isArray(detected?.interpreters) ? detected.interpreters[0] : undefined;
      if (!selected || !selected.hasVenv || selected.prefix.toLocaleLowerCase() !== selected.basePrefix.toLocaleLowerCase()) {
        setMessage("请选择支持 venv 的系统基础 Python；已有虚拟环境请使用右侧绑定入口。");
        return;
      }
      setDiscovery((current) => ({ status: "available", message: detected.message, interpreters: [selected, ...(current?.interpreters ?? []).filter((item) => item.executablePath !== selected.executablePath)] }));
      setBasePath(selected.executablePath);
    } catch (error) { setMessage(errorText(error)); }
    finally { setAction(""); }
  }

  async function chooseExistingEnvironment() {
    if (action) return;
    setAction("choose"); setMessage("");
    try {
      const detected = await backend<PythonDiscovery>("PythonFacade", "ChooseExistingEnvironment");
      if (detected?.status === "cancelled") return;
      const interpreters = Array.isArray(detected?.interpreters) ? detected.interpreters : [];
      const selected = interpreters[0];
      if (!selected || !selected.hasPip || selected.prefix.toLocaleLowerCase() === selected.basePrefix.toLocaleLowerCase()) {
        setExternalPath("");
        setMessage("请选择已有虚拟环境中的 python.exe；系统基础 Python 应由“创建项目环境”自动使用。");
        return;
      }
      setExternalPath(selected.executablePath);
      setMessage("已验证所选解释器属于现有虚拟环境；绑定后它将直接作为项目运行环境。");
    } catch (error) { setMessage(errorText(error)); }
    finally { setAction(""); }
  }

  async function mutate(kind: "create" | "bind" | "rebuild" | "verify" | "delete" | "stop") {
    if (action) return;
    if (kind === "rebuild" && !window.confirm("重建会按当前依赖锁创建新环境并原子替换现有环境。继续？")) return;
    if (kind === "bind" && !window.confirm("将所选已有虚拟环境绑定到当前项目？SciAide 只登记和验证，不会接管、修改或删除其文件。")) return;
    if (kind === "delete" && !window.confirm(environment?.environmentKind === "external" ? "解除当前外部虚拟环境绑定？原虚拟环境目录和文件不会被删除。" : "删除项目 Python 环境的派生文件？依赖锁声明仍会保留，可稍后重建。")) return;
    setAction(kind); setMessage("");
    try {
      if (kind === "create" || kind === "rebuild") {
        const updated = await backend<PythonEnvironment>("PythonFacade", "CreateProjectEnvironment", project.id, basePath, kind === "rebuild");
        setEnvironment(updated); setMessage(kind === "rebuild" ? "Python 环境已原子重建。" : "Workspace 托管 Python 环境已创建。");
      } else if (kind === "bind") {
        const updated = await backend<PythonEnvironment>("PythonFacade", "BindProjectEnvironment", project.id, externalPath);
        setEnvironment(updated); setMessage("已有虚拟环境已绑定；SciAide 不会修改或删除其文件。");
      } else if (kind === "verify") {
        const updated = await backend<PythonEnvironment>("PythonFacade", "VerifyProjectEnvironment", project.id);
        setEnvironment(updated); setMessage("解释器、pip、依赖锁和环境指纹已重新验证。");
      } else if (kind === "delete") {
        const external = environment?.environmentKind === "external";
        await backend<void>("PythonFacade", "DeleteProjectEnvironment", project.id);
        setEnvironment(await backend<PythonEnvironment>("PythonFacade", "GetProjectEnvironment", project.id));
        setMessage(external ? "已解除绑定，原虚拟环境文件保持不变。" : "环境文件已删除，依赖锁声明已保留。");
      } else {
        await backend<void>("PythonFacade", "StopProjectKernel", project.id);
        setMessage("项目 Kernel 已停止，内存变量和导入已清空。");
      }
      feedback(kind === "stop" ? "项目 Python Kernel 已停止。" : "项目 Python 环境已更新。");
    } catch (error) { setMessage(errorText(error)); }
    finally { setAction(""); }
  }

  const ready = environment?.state === "ready";
  const broken = environment?.state === "broken";
  const external = environment?.environmentKind === "external";
  const baseInterpreters = (discovery?.interpreters ?? []).filter((item) => item.hasVenv && item.prefix.toLocaleLowerCase() === item.basePrefix.toLocaleLowerCase());
  const selectedBase = baseInterpreters.find((item) => item.executablePath === basePath);
  const legacy = environment?.environmentKind === "legacy_managed" && environment?.state !== "absent";
  const kindText = external ? "外部虚拟环境" : legacy ? "旧版全局托管环境" : "Workspace 托管环境";
  const workspaceEnvironmentPath = `${project.workspacePath.replace(/[\\/]$/, "")}\\.sciaide\\python\\venv`;
  const managedActionText = legacy ? "重建旧版全局环境" : ready || broken ? "重建项目环境" : "创建项目环境";
  const stateText = ({ absent: "未创建", creating: "创建中", ready: "可用", broken: "需要修复", deleting: "删除中" } as Record<string, string>)[environment?.state ?? "absent"];
  return <div className="modal-backdrop"><section className="python-environment-modal" role="dialog" aria-modal="true" aria-labelledby="python-environment-title">
    <header><div><span className="dialog-icon python"><Icon name="tool" size={19}/></span><div><p>PROJECT RUNTIME</p><h2 id="python-environment-title">{project.name} · Python 环境</h2></div></div><button type="button" className="close" onClick={close}><Icon name="close"/></button></header>
    {loading ? <div className="python-environment-loading"><Icon name="refresh" size={22}/><b>正在检测 Python 解释器和项目环境</b></div> : <div className="python-environment-body">
      <section className={`python-runtime-status ${environment?.state ?? "absent"}`}><span><Icon name={ready ? "check" : "tool"} size={20}/></span><div><small>环境状态 · {kindText}</small><b>{stateText}</b><p>{ready ? `${environment?.implementation} ${environment?.baseExecutableVersion} · ${environment?.architecture}` : environment?.errorMessage || "当前项目还没有独立 Python 虚拟环境。"}</p></div><code>{ready ? shortHash(environment?.environmentFingerprint) : "NO ENV"}</code></section>
      {!ready && !broken && <section className="python-environment-choices">
        <article><header><span><Icon name="tool" size={16}/></span><div><b>创建项目环境</b><small>推荐 · 使用系统 Python 创建隔离环境</small></div></header>{baseInterpreters.length ? <><label>创建来源<select value={basePath} disabled={Boolean(action)} onChange={(event) => setBasePath(event.target.value)}>{baseInterpreters.map((item) => <option key={item.executableSha256} value={item.executablePath}>{item.implementation} {item.version} · {item.executablePath}</option>)}</select></label><p>基础 Python 只负责创建。后续 Shell、Python 和 Kernel 使用 <code>{workspaceEnvironmentPath}\Scripts\python.exe</code>。</p></> : <div className="python-missing"><Icon name="shield" size={16}/><div><b>未检测到可创建环境的 Python 3</b><span>请先安装带 venv 的 64 位 Python 3，或手动选择基础 python.exe。</span></div></div>}<div className="python-choice-actions"><button type="button" disabled={Boolean(action)} onClick={() => void chooseBaseInterpreter()}><Icon name="folder" size={13}/>选择基础 Python</button><button type="button" className="primary" disabled={Boolean(action) || !selectedBase} onClick={() => void mutate("create")}><Icon name={action === "create" ? "refresh" : "tool"} size={14}/>创建项目环境</button></div></article>
        <article><header><span><Icon name="folder" size={16}/></span><div><b>使用已有虚拟环境</b><small>可选 · 直接作为项目工具运行环境</small></div></header><p>SciAide 只绑定和验证，不安装依赖、不删除文件。请选择 venv 或 conda 环境中的 python.exe。</p>{externalPath && <code className="python-external-path" title={externalPath}>{externalPath}</code>}<div className="python-choice-actions"><button type="button" disabled={Boolean(action)} onClick={() => void chooseExistingEnvironment()}><Icon name="folder" size={13}/>{externalPath ? "更换虚拟环境" : "选择已有虚拟环境"}</button><button type="button" className="primary" disabled={Boolean(action) || !externalPath} onClick={() => void mutate("bind")}><Icon name={action === "bind" ? "refresh" : "check"} size={13}/>确认绑定</button></div></article>
      </section>}
      {ready && <section className="python-environment-facts"><div><span>实际项目运行解释器</span><code title={environment.environmentPythonPath}>{environment.environmentPythonPath}</code></div><div><span>环境类型</span><b>{kindText}</b><small>{external ? "用户维护文件" : "SciAide 托管派生文件"}</small></div><div><span>依赖锁</span><b>{environment.lock.length} 个包</b><small title={environment.freezeSha256}>{shortHash(environment.freezeSha256)}</small></div><div><span>{external ? "环境路径" : "创建来源（仅创建时使用）"}</span><code title={environment.baseExecutablePath}>{environment.baseExecutablePath}</code></div></section>}
      <section className="python-actions">{!external && (ready || broken) && <button type="button" className="primary" disabled={Boolean(action) || !basePath} onClick={() => void mutate("rebuild")}><Icon name={action === "rebuild" ? "refresh" : "tool"} size={14}/>{managedActionText}</button>}<button type="button" disabled={Boolean(action) || !ready && !broken} onClick={() => void mutate("verify")}><Icon name={action === "verify" ? "refresh" : "check"} size={14}/>验证运行环境</button><button type="button" disabled={Boolean(action)} onClick={() => void mutate("stop")}><Icon name="stop" size={14}/>停止 Kernel</button><button type="button" className="danger" disabled={Boolean(action) || environment?.state === "absent"} onClick={() => void mutate("delete")}><Icon name="trash" size={14}/>{external ? "解除绑定" : "删除环境文件"}</button></section>
      {ready && <details className="python-lock"><summary>查看依赖锁 <span>{environment.lock.length}</span></summary><pre>{environment.lock.join("\n") || "环境仅包含 Python 标准库。"}</pre></details>}
      <div className="python-runtime-boundary"><Icon name="shield" size={16}/><div><b>Shell、一次性 Python 和项目 Kernel 默认可以联网</b><span>不会增加域名白名单或联网弹窗；Plan 仍确认整个高风险工具调用。依赖安装因修改项目环境而确认，API Key 和 MCP Secret 不会自动传入子进程。</span></div></div>
      {message && <div className="python-environment-message"><span>{message}</span><button type="button" onClick={() => setMessage("")}><Icon name="close" size={13}/></button></div>}
    </div>}
  </section></div>;
}

const skillOriginText = (origin: SkillOrigin) => ({ default: "默认内置", installed: "Git 安装", user: "用户", project: "项目" })[origin];
const skillCapabilityText = (value: SkillCapability) => ({ native: "原生可用", requires_dependency: "需要本地依赖", requires_external_service: "需要外部服务", unavailable: "当前不可用", unreviewed: "尚未审计" })[value];
const emptySkillSnapshot: DynamicSkillSnapshot = { skills: [], categories: [], diagnostics: [], capabilities: {}, defaultCount: 0, enabledCount: 0 };
const normalizeSkillSnapshot = (value?: Partial<DynamicSkillSnapshot> | null): DynamicSkillSnapshot => ({
  skills: Array.isArray(value?.skills) ? value.skills.map((item) => ({
    ...item,
    tags: Array.isArray(item.tags) ? item.tags : [],
    routingAliases: Array.isArray(item.routingAliases) ? item.routingAliases : [],
	allowedTools: Array.isArray(item.allowedTools) ? item.allowedTools : [],
	overridden: Array.isArray(item.overridden) ? item.overridden : [],
	requiredTools: Array.isArray(item.requiredTools) ? item.requiredTools : [],
	missingTools: Array.isArray(item.missingTools) ? item.missingTools : [],
	pythonPackages: Array.isArray(item.pythonPackages) ? item.pythonPackages : [],
	cliDependencies: Array.isArray(item.cliDependencies) ? item.cliDependencies : [],
	externalServices: Array.isArray(item.externalServices) ? item.externalServices : [],
	capabilityLimitations: Array.isArray(item.capabilityLimitations) ? item.capabilityLimitations : [],
	capability: item.capability || "unreviewed",
	capabilityReason: item.capabilityReason || "尚未获得能力审计信息",
  })) : [],
  categories: Array.isArray(value?.categories) ? value.categories : [],
  diagnostics: Array.isArray(value?.diagnostics) ? value.diagnostics : [],
  capabilities: value?.capabilities && typeof value.capabilities === "object" ? value.capabilities : {},
  defaultCount: Number.isFinite(value?.defaultCount) ? value!.defaultCount! : 0,
  enabledCount: Number.isFinite(value?.enabledCount) ? value!.enabledCount! : 0,
});
const userSkillTemplate = (name = "my-research-skill") => `---\nname: ${name}\ndescription: 描述何时应使用此科研 Skill\ncategory: research\ntags: [custom]\nrouting-aliases: [English research intent, 中文科研意图]\n---\n\n# Workflow\n\nDescribe the procedure, checks, and expected outputs.\n`;

function SkillSourceBrowser({ projectId, skill, setFeedback }: { projectId: string; skill: DynamicSkill; setFeedback: (value: string) => void }) {
  const [tree, setTree] = useState<SkillSourceTree | null>(null);
  const [selectedPath, setSelectedPath] = useState("");
  const [file, setFile] = useState<SkillSourceFile | null>(null);
  const [loadingTree, setLoadingTree] = useState(true);
  const [loadingFile, setLoadingFile] = useState(false);
  const treeRequests = useRef(createLatestRequestGate());
  const fileRequests = useRef(createLatestRequestGate());

  useEffect(() => {
    const request = treeRequests.current.begin();
    setTree(null); setFile(null); setSelectedPath(""); setLoadingTree(true);
    backend<SkillSourceTree>("SkillFacade", "ListSkillSource", projectId, skill.name).then((value) => {
      if (!treeRequests.current.isCurrent(request)) return;
      const normalized = { ...value, entries: Array.isArray(value.entries) ? value.entries : [] };
      setTree(normalized);
      const firstPath = normalized.entries.find((entry) => entry.kind === "file" && entry.path === "SKILL.md")?.path
        ?? normalized.entries.find((entry) => entry.kind === "file")?.path
        ?? "";
      setSelectedPath(firstPath);
    }).catch((error) => { if (treeRequests.current.isCurrent(request)) setFeedback(errorText(error)); })
      .finally(() => { if (treeRequests.current.isCurrent(request)) setLoadingTree(false); });
    return () => { treeRequests.current.invalidate(); };
  }, [projectId, setFeedback, skill.name, skill.packageHash]);

  useEffect(() => {
    if (!selectedPath) { fileRequests.current.invalidate(); setFile(null); return; }
    const request = fileRequests.current.begin();
    setFile(null); setLoadingFile(true);
    backend<SkillSourceFile>("SkillFacade", "ReadSkillSourceFile", projectId, skill.name, selectedPath)
      .then((value) => { if (fileRequests.current.isCurrent(request)) setFile(value); })
      .catch((error) => { if (fileRequests.current.isCurrent(request)) setFeedback(errorText(error)); })
      .finally(() => { if (fileRequests.current.isCurrent(request)) setLoadingFile(false); });
    return () => { fileRequests.current.invalidate(); };
  }, [projectId, selectedPath, setFeedback, skill.name, skill.packageHash]);

  return <section className="skill-section skill-source-section">
    <div className="skill-section-title"><div><h4>源码与文件</h4><p>当前生效包的只读视图；点击文件浏览，二进制文件仅展示元数据</p></div>{tree && <code title={tree.packageHash}>{tree.entries.filter((item) => item.kind === "file").length} files · {shortHash(tree.packageHash)}</code>}</div>
    <div className="skill-source-browser">
      <div className="skill-source-tree" aria-label={`${skill.name} 文件目录`}>
        <b>{skill.name}/</b>
        {loadingTree ? <span className="skill-source-loading">正在读取目录…</span> : tree?.entries.length ? tree.entries.map((entry, index) => {
          const name = entry.path.split("/").at(-1) ?? entry.path;
          const label = `${skillTreePrefix(tree.entries, index)}${name}${entry.kind === "directory" ? "/" : ""}`;
          return entry.kind === "directory"
            ? <span className="skill-tree-directory" key={entry.path}>{label}</span>
            : <button type="button" className={selectedPath === entry.path ? "selected" : ""} onClick={() => setSelectedPath(entry.path)} key={entry.path} title={`${entry.path} · ${fileSize(entry.size)}`}>{label}</button>;
        }) : <span className="skill-source-loading">目录为空。</span>}
      </div>
      <div className="skill-source-viewer">
        {loadingFile ? <div className="skill-source-empty">正在读取文件…</div> : file ? <>
          <header><code title={file.path}>{file.path}</code><span>{file.mediaType || "application/octet-stream"} · {fileSize(file.originalBytes)}</span></header>
          {file.text ? <pre><code>{file.content}</code></pre> : <div className="skill-source-empty"><Icon name="archive" size={20}/><b>文件不可作为文本预览</b><span>二进制或超大文件仅展示元数据，原始字节不会载入查看器。</span></div>}
        </> : <div className="skill-source-empty">选择一个文件查看内容。</div>}
      </div>
    </div>
  </section>;
}

function SkillSettings({ project, close }: { project?: Project; close: () => void }) {
  const [snapshot, setSnapshot] =
    useState<DynamicSkillSnapshot>(emptySkillSnapshot);
  const [selectedName, setSelectedName] = useState("");
  const [panel, setPanel] = useState<"catalog" | "git" | "user">("catalog");
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState("all");
  const [origin, setOrigin] = useState<"all" | SkillOrigin>("all");
  const [capability, setCapability] = useState<"all" | SkillCapability>("all");
  const [gitURL, setGitURL] = useState("");
  const [gitResult, setGitResult] = useState<SkillGitInstallResult | null>(
    null,
  );
  const [userName, setUserName] = useState("my-research-skill");
  const [userContent, setUserContent] = useState(userSkillTemplate());
  const [editingUser, setEditingUser] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [toast, setToast] = useState<{
    id: number;
    text: string;
    detail: string;
  } | null>(null);

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return snapshot.skills.filter((item) => {
      if (category !== "all" && (item.category || "other") !== category)
        return false;
      if (origin !== "all" && item.origin !== origin) return false;
      if (capability !== "all" && item.capability !== capability) return false;
      if (!needle) return true;
      return `${item.name} ${item.description} ${item.category || "other"} ${item.tags.join(" ")} ${item.routingAliases.join(" ")}`
        .toLowerCase()
        .includes(needle);
    });
  }, [capability, category, origin, query, snapshot.skills]);
  const selected = snapshot.skills.find((item) => item.name === selectedName);

  const load = useCallback(async () => {
    const value = normalizeSkillSnapshot(
      await backend<DynamicSkillSnapshot>(
        "SkillFacade",
        "ListSkills",
        project?.id ?? "",
      ),
    );
    setSnapshot(value);
    setSelectedName((current) =>
      value.skills.some((item) => item.name === current)
        ? current
        : (value.skills[0]?.name ?? ""),
    );
  }, [project]);

  useEffect(() => {
    setLoading(true);
    load()
      .catch((error: unknown) => setFeedback(errorText(error)))
      .finally(() => setLoading(false));
  }, [load]);
  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(null), 2800);
    return () => window.clearTimeout(timer);
  }, [toast]);
  useEffect(() => {
    if (
      panel !== "catalog" ||
      filtered.some((item) => item.name === selectedName)
    )
      return;
    setSelectedName(filtered[0]?.name ?? "");
  }, [filtered, panel, selectedName]);

  function showToast(text: string, detail: string) {
    setToast({ id: Date.now(), text, detail });
  }

  async function refreshCatalog() {
    setBusy(true);
    setFeedback("");
    try {
      const result = normalizeSkillSnapshot(
        await backend<DynamicSkillSnapshot>(
          "SkillFacade",
          "RefreshDynamicSkills",
          project?.id ?? "",
        ),
      );
      setSnapshot(result);
      showToast(
        "Skill 目录已刷新",
        `${result.skills.length} 个 Skill · ${result.enabledCount} 个允许加载`,
      );
      if (result.diagnostics.length) setFeedback(result.diagnostics.join("；"));
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  async function setAllSkills(enabled: boolean) {
    setBusy(true);
    setFeedback("");
    try {
      const result = await backend<SkillPolicyBatchResult>(
        "SkillFacade",
        "SetAllSkillsEnabled",
        project?.id ?? "",
        enabled,
      );
      await load();
      showToast(
        enabled ? "已允许加载全部 Skill" : "已禁止加载全部 Skill",
        `变更 ${result.changed} · 未变 ${result.unchanged}`,
      );
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  async function runGitInstall(request: {
    url: string;
    replace: boolean;
    allowWarnings: boolean;
    expectedSha?: string;
  }) {
    try {
      return await backend<SkillGitInstallResult>(
        "SkillFacade",
        "InstallGitSkills",
        request,
      );
    } catch (error) {
      const message = errorText(error);
      if (
        !request.replace &&
        message.includes("explicit replacement is required") &&
        window.confirm(
          "该 Git namespace 已存在且内容不同。\n\n替换前版本会进入可恢复归档；已开始的 Run 仍使用原有快照。确认替换？",
        )
      ) {
        return backend<SkillGitInstallResult>(
          "SkillFacade",
          "InstallGitSkills",
          { ...request, replace: true },
        );
      }
      throw error;
    }
  }

  async function installGit(event: FormEvent) {
    event.preventDefault();
    if (!gitURL.trim()) return;
    setBusy(true);
    setFeedback("");
    try {
      const result = await runGitInstall({
        url: gitURL.trim(),
        replace: false,
        allowWarnings: false,
      });
      setGitResult(result);
      if (!result.reviewRequired && result.installed.length > 0) {
        await load();
        showToast(
          result.replaced
            ? "Git Skill 已替换"
            : result.idempotent
              ? "Git Skill 已存在"
              : "Git Skill 已安装",
          `${result.installed.length} 个 Skill · ${shortHash(result.pinnedSha)}`,
        );
      } else if (!result.reviewRequired && result.installed.length === 0)
        setFeedback(
          result.rejected.length
            ? "仓库中的 Skill 均未通过本地安全审查。"
            : "仓库中没有找到可安装的 SKILL.md。 ",
        );
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  async function confirmGitWarnings() {
    if (!gitResult?.reviewRequired || !gitResult.pinnedSha) return;
    setBusy(true);
    setFeedback("");
    try {
      const result = await runGitInstall({
        url: gitURL.trim(),
        replace: false,
        allowWarnings: true,
        expectedSha: gitResult.pinnedSha,
      });
      setGitResult(result);
      await load();
      showToast(
        "审查确认完成",
        `已安装 ${result.installed.length} 个 · 固定 ${shortHash(result.pinnedSha)}`,
      );
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  async function toggleSkill(item: DynamicSkill) {
    setBusy(true);
    setFeedback("");
    try {
      const updated = await backend<DynamicSkill>(
        "SkillFacade",
        "SetSkillEnabled",
        project?.id ?? "",
        item.name,
        !item.enabled,
      );
      await load();
      showToast(
        updated.enabled ? "已允许模型加载" : "已禁止模型加载",
        updated.name,
      );
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  async function openUserEditor(item?: DynamicSkill) {
    setFeedback("");
    setPanel("user");
    if (!item) {
      setEditingUser(false);
      setUserName("my-research-skill");
      setUserContent(userSkillTemplate());
      return;
    }
    setBusy(true);
    try {
      setUserName(item.name);
      setUserContent(
        await backend<string>("SkillFacade", "ReadUserSkill", item.name),
      );
      setEditingUser(true);
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  async function saveUserSkill(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setFeedback("");
    try {
      const result = await backend<DynamicSkill>(
        "SkillFacade",
        "WriteUserSkill",
        userName.trim(),
        userContent,
      );
      await load();
      setSelectedName(result.name);
      setPanel("catalog");
      showToast(
        editingUser ? "User Skill 已更新" : "User Skill 已创建",
        result.name,
      );
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  async function deleteUserSkill(item: DynamicSkill) {
    if (
      !window.confirm(
        `删除 User Skill “${item.name}”？\n\n目录会移入可恢复归档；同名低优先级 Skill 会重新生效。`,
      )
    )
      return;
    setBusy(true);
    setFeedback("");
    try {
      await backend<void>("SkillFacade", "DeleteUserSkill", item.name);
      await load();
      showToast("User Skill 已删除", "已移入可恢复归档");
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  async function removeInstalledSkill(item: DynamicSkill) {
    if (
      !item.namespace ||
      !window.confirm(
        `卸载 Git Skill “${item.name}”？\n\n文件会移入可恢复归档；同名低优先级 Skill 会重新生效。`,
      )
    )
      return;
    setBusy(true);
    setFeedback("");
    try {
      const result = await backend<SkillRemoveResult>(
        "SkillFacade",
        "RemoveInstalledSkill",
        item.namespace,
        item.name,
      );
      await load();
      showToast("Git Skill 已卸载", `${result.archived} 个 Skill 已归档`);
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="modal-backdrop">
      <section
        className="model-modal skill-modal"
        role="dialog"
        aria-modal="true"
      >
        {toast && (
          <div className="mcp-toast" role="status">
            <span>
              <Icon name="check" size={15} />
            </span>
            <div>
              <b>{toast.text}</b>
              <small>{toast.detail}</small>
            </div>
          </div>
        )}
        <header>
          <div>
            <span className="dialog-icon gradient">
              <Icon name="skill" />
            </span>
            <div>
              <p>SCIAIDE SKILL CATALOG</p>
              <h2>Skills</h2>
            </div>
          </div>
          <button className="close" onClick={close} aria-label="关闭">
            <Icon name="close" />
          </button>
        </header>
        <div className="settings-grid">
          <aside>
            <div className="skill-create-actions">
              <button
                className={panel === "git" ? "selected" : ""}
                onClick={() => {
                  setPanel("git");
                  setFeedback("");
                }}
                disabled={busy}
              >
                <Icon name="download" size={14} /> Git 安装
              </button>
              <button
                className={panel === "user" && !editingUser ? "selected" : ""}
                onClick={() => void openUserEditor()}
                disabled={busy}
              >
                <Icon name="plus" size={14} /> 新建
              </button>
            </div>
            <div className="skill-catalog-actions">
              <button
                className="skill-enable-all"
                onClick={() => void setAllSkills(true)}
                disabled={busy || !snapshot.skills.length}
              >
                <Icon name="check" size={14} /> 全开
              </button>
              <button
                className="skill-disable-all"
                onClick={() => void setAllSkills(false)}
                disabled={busy || !snapshot.skills.length}
              >
                <Icon name="stop" size={14} /> 全关
              </button>
              <button
                className="skill-refresh"
                onClick={() => void refreshCatalog()}
                disabled={busy}
              >
                <Icon name="refresh" size={14} />
              </button>
            </div>
            <div className="skill-filter-stack">
              <label className="skill-search">
                <Icon name="search" size={13} />
                <input
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  placeholder="搜索名称、描述或标签"
                />
              </label>
              <div>
                <select
                  value={category}
                  onChange={(event) => setCategory(event.target.value)}
                >
                  <option value="all">全部分类</option>
                  {snapshot.categories.map((item) => (
                    <option value={item.name} key={item.name}>
                      {item.name} ({item.count})
                    </option>
                  ))}
                </select>
                <select
                  value={origin}
                  onChange={(event) =>
                    setOrigin(event.target.value as "all" | SkillOrigin)
                  }
                >
                  <option value="all">全部来源</option>
                  <option value="project">项目</option>
                  <option value="user">用户</option>
                  <option value="installed">Git 安装</option>
                  <option value="default">默认内置</option>
                </select>
                <select
                  value={capability}
                  onChange={(event) => setCapability(event.target.value as "all" | SkillCapability)}
                >
                  <option value="all">全部能力</option>
                  {(["native", "requires_dependency", "requires_external_service", "unavailable", "unreviewed"] as SkillCapability[]).map((item) => (
                    <option value={item} key={item}>{skillCapabilityText(item)} ({snapshot.capabilities[item] ?? 0})</option>
                  ))}
                </select>
              </div>
            </div>
            <div className="profile-caption">
              目录 · {filtered.length}/{snapshot.skills.length}
            </div>
            <div className="skill-side-list">
              {loading ? (
                <p className="skill-side-empty">正在读取动态目录…</p>
              ) : filtered.length ? (
                filtered.map((item) => (
                  <button
                    key={item.name}
                    className={`profile-item skill-profile ${panel === "catalog" && item.name === selectedName ? "selected" : ""}`}
                    onClick={() => {
                      setPanel("catalog");
                      setSelectedName(item.name);
                      setFeedback("");
                    }}
                    disabled={busy}
                  >
                    <span className={`provider-logo origin-${item.origin}`}>
                      <Icon name="skill" size={15} />
                    </span>
                    <span>
                      <b>{item.name}</b>
                      <small>
                        {item.category || "other"} ·{" "}
                        {skillOriginText(item.origin)}
                      </small>
                    </span>
                    <i
                      className={`status-dot ${item.enabled ? "ready" : ""}`}
                    />
                  </button>
                ))
              ) : (
                <p className="skill-side-empty">没有符合筛选条件的 Skill。</p>
              )}
            </div>
          </aside>
          {panel === "git" ? (
            <form
              className="skill-install"
              onSubmit={(event) => void installGit(event)}
            >
              <section className="form-section">
                <div className="form-heading">
                  <span>01</span>
                  <div>
                    <h3>从 Git 安装 Skill</h3>
                    <p>
                      公共 HTTPS 仓库或 gh:owner/repo，可固定 ref 和仓库子目录
                    </p>
                  </div>
                </div>
                <label>
                  Git 来源
                  <input
                    value={gitURL}
                    onChange={(event) => {
                      setGitURL(event.target.value);
                      setGitResult(null);
                    }}
                    placeholder="https://github.com/owner/repo.git 或 gh:owner/repo"
                    required
                    disabled={busy}
                  />
                  <small>
                    仓库会在两分钟超时内无凭据克隆，审查完成后固定到 commit
                    SHA，再原子发布。
                  </small>
                </label>
                <div className="skill-security-note">
                  <Icon name="shield" size={17} />
                  <div>
                    <b>Skill 与仓库内容均是不可信数据</b>
                    <span>
                      安装器拒绝路径逃逸和高危模式；警告项必须绑定审查 SHA
                      二次确认。脚本只允许模型按文本读取，SciAide 不会自动执行。
                    </span>
                  </div>
                </div>
                {gitResult && (
                  <div className="skill-review-result">
                    <header>
                      <b>本地审查</b>
                      <code>{shortHash(gitResult.pinnedSha)}</code>
                    </header>
                    {gitResult.rejected.length > 0 && (
                      <section className="rejected">
                        <b>拒绝 · {gitResult.rejected.length}</b>
                        {gitResult.rejected.map((item) => (
                          <p key={`${item.name}:${item.reason}`}>
                            <strong>{item.name}</strong>
                            <span>{item.reason}</span>
                          </p>
                        ))}
                      </section>
                    )}
                    {gitResult.warnings.length > 0 && (
                      <section className="warnings">
                        <b>警告 · {gitResult.warnings.length}</b>
                        {gitResult.warnings.slice(0, 20).map((item, index) => (
                          <p
                            key={`${item.name}:${item.file}:${item.line}:${index}`}
                          >
                            <strong>
                              {item.name} · {item.file}:{item.line}
                            </strong>
                            <span>
                              {item.pattern} · {item.snippet}
                            </span>
                          </p>
                        ))}
                      </section>
                    )}
                    {gitResult.installed.length > 0 && (
                      <section className="accepted">
                        <b>
                          {gitResult.idempotent ? "已存在" : "已安装"} ·{" "}
                          {gitResult.installed.length}
                        </b>
                        {gitResult.installed.map((item) => (
                          <p key={item.name}>
                            <strong>{item.name}</strong>
                            <span>
                              {item.verdict} · {shortHash(item.packageHash)}
                            </span>
                          </p>
                        ))}
                      </section>
                    )}
                    {gitResult.reviewRequired && (
                      <button
                        type="button"
                        className="skill-review-confirm"
                        onClick={() => void confirmGitWarnings()}
                        disabled={busy}
                      >
                        <Icon name="shield" size={14} /> 确认同一 SHA
                        的警告并安装
                      </button>
                    )}
                  </div>
                )}
              </section>
              {feedback && <div className="feedback error">{feedback}</div>}
              <footer className="modal-actions">
                <span />
                <span />
                <button
                  type="button"
                  onClick={() => {
                    setGitURL("");
                    setGitResult(null);
                    setFeedback("");
                  }}
                  disabled={busy || (!gitURL && !gitResult)}
                >
                  清空
                </button>
                <button
                  className="primary"
                  disabled={
                    busy || !gitURL.trim() || Boolean(gitResult?.reviewRequired)
                  }
                >
                  {busy ? "审查中…" : "审查并安装"}
                </button>
              </footer>
            </form>
          ) : panel === "user" ? (
            <form
              className="skill-user-editor"
              onSubmit={(event) => void saveUserSkill(event)}
            >
              <section className="form-section">
                <div className="form-heading">
                  <span>01</span>
                  <div>
                    <h3>
                      {editingUser ? `编辑 ${userName}` : "新建 User Skill"}
                    </h3>
                    <p>
                      保存到 SciAide 用户目录，优先级高于 Git 安装和默认内置
                    </p>
                  </div>
                </div>
                <label>
                  Skill 名称
                  <input
                    value={userName}
                    onChange={(event) => {
                      const value = event.target.value;
                      setUserName(value);
                      if (!editingUser)
                        setUserContent((current) =>
                          current.replace(/^name:\s*.*$/m, `name: ${value}`),
                        );
                    }}
                    pattern="[A-Za-z0-9][A-Za-z0-9_-]{0,63}"
                    required
                    disabled={busy || editingUser}
                  />
                </label>
                <label>
                  SKILL.md
                  <textarea
                    className="skill-markdown-editor"
                    value={userContent}
                    onChange={(event) => setUserContent(event.target.value)}
                    spellCheck={false}
                    required
                    disabled={busy}
                  />
                  <small>
                    必须包含 name、description frontmatter
                    和非空正文。内容不能授予工具权限或绕过 Plan/Full Access。
                  </small>
                </label>
                <div className="skill-security-note">
                  <Icon name="shield" size={17} />
                  <div>
                    <b>保存前执行本地结构与安全校验</b>
                    <span>
                      同名 User Skill 会覆盖低优先级来源；已加载到 Run
                      的完整正文快照保持不变。
                    </span>
                  </div>
                </div>
              </section>
              {feedback && <div className="feedback error">{feedback}</div>}
              <footer className="modal-actions">
                <span />
                <span />
                <button
                  type="button"
                  onClick={() => {
                    setPanel("catalog");
                    setFeedback("");
                  }}
                  disabled={busy}
                >
                  取消
                </button>
                <button
                  className="primary"
                  disabled={busy || !userName.trim() || !userContent.trim()}
                >
                  {busy ? "保存中…" : "保存 User Skill"}
                </button>
              </footer>
            </form>
          ) : (
            <div className="skill-detail">
              {loading ? (
                <div className="skill-page-state">正在加载动态 Skill 目录…</div>
              ) : selected ? (
                <>
                  <section className="skill-overview">
                    <div className="skill-title">
                      <span
                        className={`skill-large-icon origin-${selected.origin}`}
                      >
                        <Icon name="skill" size={22} />
                      </span>
                      <div>
                        <div>
                          <h3>{selected.name}</h3>
                          <code>{selected.category || "other"}</code>
                        </div>
                        <p>{selected.description}</p>
                      </div>
                    </div>
                    <div className="skill-state-row">
                      <span className={`skill-badge origin-${selected.origin}`}>
                        {skillOriginText(selected.origin)}
                      </span>
                      <span
                        className={`skill-badge ${selected.enabled && selected.capability !== "unavailable" ? "available" : "unavailable"}`}
                      >
                        {selected.capability === "unavailable" ? "当前不可加载" : selected.enabled ? "允许模型加载" : "禁止模型加载"}
                      </span>
                      <span className="skill-badge neutral">
                        {selected.entry ? "用户入口" : "内部辅助"}
                      </span>
                      {selected.reviewVerdict && (
                        <span
                          className={`skill-badge ${selected.reviewVerdict === "pass" ? "valid" : "warning"}`}
                        >
                          审查 · {selected.reviewVerdict}
                        </span>
                      )}
                      <span className={`skill-badge capability-${selected.capability}`} title={selected.capabilityReason}>
                        {skillCapabilityText(selected.capability)}
                      </span>
                    </div>
                  </section>
                  <section className="skill-section skill-policy-section">
                    <div className="skill-section-title">
                      <div>
                        <h4>模型加载策略</h4>
                        <p>
                          模型按当前任务语义选择 Skill；正文仅在调用
                          builtin.skill.load 后按需进入本 Run
                        </p>
                      </div>
                      <label className="skill-policy-toggle">
                        <input
                          type="checkbox"
                          checked={selected.enabled}
                          onChange={() => void toggleSkill(selected)}
                          disabled={busy}
                        />
                        <span>
                          <b>允许模型加载</b>
                          <small>
                            按 Skill 名称全局生效；项目同名覆盖沿用此策略
                          </small>
                        </span>
                      </label>
                    </div>
                  </section>
                  <section className={`skill-capability-note capability-${selected.capability}`}>
                    <Icon name={selected.capability === "native" ? "check" : "shield"} size={16} />
                    <div>
                      <b>{skillCapabilityText(selected.capability)}{selected.capabilityAuditVersion ? ` · ${selected.capabilityAuditVersion}` : ""}</b>
                      <span>{selected.capabilityReason}</span>
                    </div>
                  </section>
                  {(selected.requiredTools.length > 0 || selected.pythonPackages.length > 0 || selected.cliDependencies.length > 0 || selected.externalServices.length > 0 || selected.capabilityLimitations.length > 0) && (
                    <section className="skill-section skill-audit-detail">
                      <div className="skill-section-title"><div><h4>能力审计</h4><p>固定声明会与当前运行时已注册工具实时校验</p></div></div>
                      {selected.requiredTools.length > 0 && <div className="skill-chips"><b>所需工具</b>{selected.requiredTools.map((item) => <span className={selected.missingTools.includes(item) ? "missing" : ""} key={item}>{item}</span>)}</div>}
                      {selected.pythonPackages.length > 0 && <div className="skill-chips muted"><b>Python 包</b>{selected.pythonPackages.map((item) => <span key={item}>{item}</span>)}</div>}
                      {selected.cliDependencies.length > 0 && <div className="skill-chips muted"><b>命令行依赖</b>{selected.cliDependencies.map((item) => <span key={item}>{item}</span>)}</div>}
                      {selected.externalServices.length > 0 && <div className="skill-chips muted"><b>外部服务</b>{selected.externalServices.map((item) => <span key={item}>{item}</span>)}</div>}
                      {selected.capabilityLimitations.length > 0 && <div className="skill-audit-limitations">{selected.capabilityLimitations.map((item) => <p key={item}>{item}</p>)}</div>}
                    </section>
                  )}
                  <section className="skill-section">
                    <div className="skill-section-title">
                      <div>
                        <h4>来源与覆盖</h4>
                        <p>
                          解析优先级：project &gt; user &gt; installed &gt;
                          default
                        </p>
                      </div>
                    </div>
                    <div className="skill-facts">
                      <div>
                        <span>当前来源</span>
                        <b>{skillOriginText(selected.origin)}</b>
                      </div>
                      <div>
                        <span>指令正文</span>
                        <b>{selected.instructionRunes.toLocaleString()} 字符</b>
                      </div>
                      <div>
                        <span>Package SHA256</span>
                        <b title={selected.packageHash}>
                          {shortHash(selected.packageHash)}
                        </b>
                      </div>
                    </div>
                    {selected.overridden.length > 0 && (
                      <div className="skill-chips">
                        <b>已覆盖来源</b>
                        {selected.overridden.map((item) => (
                          <span key={item}>{skillOriginText(item)}</span>
                        ))}
                      </div>
                    )}
                    {selected.repoUrl && (
                      <div className="skill-source-record">
                        <span>Repository</span>
                        <code title={selected.repoUrl}>{selected.repoUrl}</code>
                        <span>固定 SHA</span>
                        <code title={selected.pinnedSha}>
                          {selected.pinnedSha || "未记录"}
                        </code>
                      </div>
                    )}
                  </section>
                  <section className="skill-section">
                    <div className="skill-section-title">
                      <div>
                        <h4>包内容</h4>
                        <p>
                          references、assets 与 scripts 只能在 Skill
                          已加载后通过受限文本工具读取
                        </p>
                      </div>
                    </div>
                    <div className="skill-package-stats">
                      <span>
                        <b>{selected.fileCount}</b>
                        <small>文件</small>
                      </span>
                      <span>
                        <b>{selected.referenceCount}</b>
                        <small>资料</small>
                      </span>
                      <span>
                        <b>{selected.assetCount}</b>
                        <small>素材</small>
                      </span>
                      <span>
                        <b>{selected.scriptCount}</b>
                        <small>脚本源码</small>
                      </span>
                    </div>
                    {selected.tags.length > 0 && (
                      <div className="skill-chips">
                        <b>Tags</b>
                        {selected.tags.map((item) => (
                          <span key={item}>{item}</span>
                        ))}
                      </div>
                    )}
                    {selected.allowedTools.length > 0 && (
                      <div className="skill-chips muted">
                        <b>Allowed tools（仅元数据）</b>
                        {selected.allowedTools.map((item) => (
                          <span key={item}>{item}</span>
                        ))}
                      </div>
                    )}
                  </section>
                  <SkillSourceBrowser
                    projectId={project?.id ?? ""}
                    skill={selected}
                    setFeedback={setFeedback}
                  />
                  <footer className="skill-detail-actions">
                    <div>
                      {selected.origin === "user" && (
                        <>
                          <button
                            type="button"
                            onClick={() => void openUserEditor(selected)}
                            disabled={busy}
                          >
                            <Icon name="settings" size={14} /> 编辑
                          </button>
                          <button
                            type="button"
                            className="danger"
                            onClick={() => void deleteUserSkill(selected)}
                            disabled={busy}
                          >
                            <Icon name="trash" size={14} /> 删除
                          </button>
                        </>
                      )}
                      {selected.origin === "installed" && (
                        <button
                          type="button"
                          className="danger"
                          onClick={() => void removeInstalledSkill(selected)}
                          disabled={busy}
                        >
                          <Icon name="trash" size={14} /> 可恢复卸载
                        </button>
                      )}
                      {selected.origin === "default" && (
                        <span>
                          <Icon name="shield" size={14} /> SciAide 默认 Skill
                          随 EXE 内置，不写入用户目录
                        </span>
                      )}
                      {selected.origin === "project" && (
                        <span>
                          <Icon name="folder" size={14} /> 来自当前项目
                          Workspace
                        </span>
                      )}
                    </div>
                    <div className="skill-folder-actions">
                      <button
                        type="button"
                        onClick={() =>
                          void backend<void>(
                            "SkillFacade",
                            "OpenUserSkillsFolder",
                          ).catch((error) => setFeedback(errorText(error)))
                        }
                      >
                        <Icon name="folder" size={13} /> User
                      </button>
                      <button
                        type="button"
                        onClick={() =>
                          void backend<void>(
                            "SkillFacade",
                            "OpenInstalledSkillsFolder",
                          ).catch((error) => setFeedback(errorText(error)))
                        }
                      >
                        <Icon name="folder" size={13} /> Installed
                      </button>
                    </div>
                  </footer>
                </>
              ) : (
                <div className="skill-page-state">
                  <span className="skill-large-icon">
                    <Icon name="skill" size={22} />
                  </span>
                  <b>没有可显示的 Skill</b>
                  <p>调整左侧搜索或筛选条件，或创建一个 User Skill。</p>
                </div>
              )}
              {feedback && <div className="feedback error">{feedback}</div>}
              {snapshot.diagnostics.length > 0 && (
                <div className="skill-diagnostics">
                  <b>目录诊断 · {snapshot.diagnostics.length}</b>
                  {snapshot.diagnostics.slice(0, 8).map((item, index) => (
                    <p className="warning" key={`${item}:${index}`}>
                      {item}
                    </p>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      </section>
    </div>
  );
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

const emptyWorkflowTemplate: WorkflowTemplate = { id: "empty", name: "新研究方案", description: "空白研究方案", definition: { schemaVersion: 1, name: "新研究方案", description: "", inputs: [], nodes: [], edges: [], outputs: [] } };

const workflowRunLabels: Record<WorkflowRunStatus, string> = {
  queued: "排队中", running: "执行中", waiting_approval: "等待授权", waiting_human_confirmation: "等待人工确认",
  paused: "已暂停", completed: "已完成", failed: "失败", cancelled: "已取消", interrupted: "已中断",
};
const workflowStepLabels: Record<WorkflowStepStatus, string> = {
  queued: "待执行", running: "执行中", waiting_approval: "等待授权", waiting_human_confirmation: "等待人工确认",
  completed: "已完成", failed: "失败", cancelled: "已取消", interrupted: "已中断", outcome_unknown: "结果未知",
};
const workflowTerminal = (status: WorkflowRunStatus) => ["completed", "failed", "cancelled", "interrupted"].includes(status);
const workflowJSON = (value: unknown) => JSON.stringify(value ?? {}, null, 2);
const workflowDefaultInput = (port: WorkflowPort) => port.default !== undefined
  ? (typeof port.default === "string" ? port.default : workflowJSON(port.default))
  : port.type === "boolean" ? "false" : ["object", "array", "artifacts", "citations"].includes(port.type) ? (port.type === "object" ? "{}" : "[]") : "";

function effectiveWorkflowDefinition(version?: WorkflowVersion): WorkflowDefinition | null {
  if (!version) return null;
	return version.runtimeInputs?.length ? { ...version.definition, inputs: version.runtimeInputs } : version.definition;
}

const workflowCandidates = (step: WorkflowStep): WorkflowCandidate[] => {
  const values = step.input?.candidates;
  if (!Array.isArray(values)) return [];
  return values.filter((value): value is WorkflowCandidate => Boolean(value && typeof value === "object" && typeof (value as WorkflowCandidate).id === "string"));
};

const workflowCitations = (step: WorkflowStep): WorkflowCitation[] => {
  const values = step.input?.candidates;
  if (!Array.isArray(values)) return [];
  return values.filter((value): value is WorkflowCitation => Boolean(value && typeof value === "object" && typeof (value as WorkflowCitation).id === "string"));
};

const workflowHash = (value?: string) => value ? value.slice(0, 12) : "未提交";

function WorkflowRunEvidence({ detail }: { detail: WorkflowRunDetail }) {
  const evidence = deriveWorkflowRunEvidence(detail);
  const analysis = evidence.analyses.at(-1);
  const parents = (node: WorkflowEvidenceNode) => evidence.graph.edges
    .filter((edge) => edge.to === node.id)
    .map((edge) => ({ ...edge, source: evidence.graph.nodes.find((candidate) => candidate.id === edge.from) }))
    .filter((edge) => edge.source);
  return <section className="workflow-run-evidence">
    <h3>运行证据</h3>
    <div className="workflow-run-evidence-grid">
      <article className="workflow-environment-manifest">
        <header><div><Icon name="tool" size={15}/><span><b>环境 Manifest</b><small>来自已提交环境与 Kernel 步骤</small></span></div><em>{evidence.environment ? "已冻结" : "等待环境"}</em></header>
        {evidence.environment ? <dl>
          <div><dt>解释器</dt><dd>{[evidence.environment.implementation, evidence.environment.version, evidence.environment.architecture].filter(Boolean).join(" · ")}</dd></div>
          <div><dt>环境指纹</dt><dd><code title={evidence.environment.environmentFingerprint}>{workflowHash(evidence.environment.environmentFingerprint)}</code></dd></div>
          <div><dt>依赖冻结</dt><dd><code title={evidence.environment.freezeSha256}>{workflowHash(evidence.environment.freezeSha256)}</code><span>{evidence.environment.lockCount} 个包</span></dd></div>
          <div><dt>基础解释器</dt><dd><code title={evidence.environment.baseExecutableSha256}>{workflowHash(evidence.environment.baseExecutableSha256)}</code></dd></div>
          <div><dt>Kernel 复现</dt><dd>{analysis ? <><code title={analysis.reproductionSha256}>{workflowHash(analysis.reproductionSha256)}</code><span>{analysis.inputHashes.length} 输入 · {analysis.outputHashes.length} 输出</span></> : <span>等待分析步骤提交</span>}</dd></div>
        </dl> : <p>环境步骤尚未完成；SciAide 不会从当前电脑配置推测本次 Run 的环境。</p>}
      </article>
      <article className="workflow-artifact-lineage">
        <header><div><Icon name="archive" size={15}/><span><b>产物图谱</b><small>Step Output 中冻结的来源与派生关系</small></span></div><em>{evidence.graph.nodes.length} 节点 · {evidence.graph.edges.length} 边</em></header>
        {evidence.graph.nodes.length ? <div className="workflow-artifact-graph">{evidence.graph.nodes.map((node) => <div className={`workflow-evidence-node ${node.stage}`} key={node.id}>
          <i>{node.stage === "evidence" ? "证据" : node.stage === "report" ? "报告" : node.stage === "export" ? "导出" : "产物"}</i>
          <span><b>{node.label}</b><small>{node.detail || node.sourceStepName}</small>{node.sha256 && <code title={node.sha256}>SHA256 {workflowHash(node.sha256)}</code>}{parents(node).map((edge) => <em key={`${edge.from}:${edge.label}`}>← {edge.label} · {edge.source?.label}</em>)}</span>
        </div>)}</div> : <p>尚无已提交产物；运行失败或未到产物步骤时不会生成占位来源。</p>}
      </article>
    </div>
  </section>;
}

function WorkflowHumanDecision({ step, prompt, note, context, selectedCandidateIds, selectedCitationKeys, busy, setNote, setContext, setSelectedCandidateIds, setSelectedCitationKeys, decide }: {
  step: WorkflowStep;
  prompt?: string;
  note: string;
  context: string;
  selectedCandidateIds: string[];
  selectedCitationKeys: string[];
  busy: string;
  setNote: (value: string) => void;
  setContext: (value: string) => void;
  setSelectedCandidateIds: (value: string[]) => void;
  setSelectedCitationKeys: (value: string[]) => void;
  decide: (approved: boolean) => void;
}) {
  const candidates = workflowCandidates(step);
  const citations = workflowCitations(step);
  const toggle = (values: string[], value: string, update: (next: string[]) => void) => update(values.includes(value) ? values.filter((item) => item !== value) : [...values, value]);
  const citationKey = (value: WorkflowCitation) => JSON.stringify(value);
  const selection = step.nodeKind === "candidate_selection" || step.nodeKind === "citation_selection";
  const selectedCount = step.nodeKind === "candidate_selection" ? selectedCandidateIds.length : selectedCitationKeys.length;
  const totalCount = step.nodeKind === "candidate_selection" ? candidates.length : citations.length;
  return <div className="workflow-human-decision">
    <p>{prompt || (step.nodeKind === "candidate_selection" ? "请选择需要导入本地的文献候选。" : step.nodeKind === "citation_selection" ? "请选择报告使用的可信本地证据。" : "请确认后继续。")}</p>
    {selection && <div className="workflow-selection-toolbar"><span>已选 {selectedCount} / {totalCount}</span><nav><button type="button" onClick={() => step.nodeKind === "candidate_selection" ? setSelectedCandidateIds(candidates.map((value) => value.id)) : setSelectedCitationKeys(citations.map(citationKey))}>全选</button><button type="button" onClick={() => step.nodeKind === "candidate_selection" ? setSelectedCandidateIds([]) : setSelectedCitationKeys([])}>清空</button></nav></div>}
    {step.nodeKind === "candidate_selection" && <div className="workflow-selection-list">{candidates.length ? candidates.map((value) => {
      const checked = selectedCandidateIds.includes(value.id);
      const authors = (value.authors ?? []).map((author) => author.name).filter(Boolean).slice(0, 5).join("、");
      return <label key={value.id} className={checked ? "selected" : ""}><input type="checkbox" checked={checked} onChange={() => toggle(selectedCandidateIds, value.id, setSelectedCandidateIds)}/><span><b>{value.title || "未命名候选"}</b><small>{[authors, value.year, value.venue].filter(Boolean).join(" · ") || value.id}</small>{value.abstract && <p>{value.abstract}</p>}<em>{value.sourceIds?.join(" / ") || "公共数据库"}{value.doi ? ` · DOI ${value.doi}` : ""}{value.openAccess ? " · 开放获取" : ""}</em></span></label>;
    }) : <div className="workflow-selection-empty">没有可供筛选的候选，不能继续。</div>}</div>}
    {step.nodeKind === "citation_selection" && <div className="workflow-selection-list citations">{citations.length ? citations.map((value, index) => {
      const key = citationKey(value);
      const checked = selectedCitationKeys.includes(key);
      return <label key={`${value.reference || value.id}:${index}`} className={checked ? "selected" : ""}><input type="checkbox" checked={checked} onChange={() => toggle(selectedCitationKeys, key, setSelectedCitationKeys)}/><span><b>{value.title || value.sourceName || "本地证据"}</b><small>{[value.sourceName, value.locator, value.reference].filter(Boolean).join(" · ")}</small><p>{value.quote || "无摘录"}</p></span></label>;
    }) : <div className="workflow-selection-empty">本地检索没有返回可引用证据，不能继续。</div>}</div>}
    <label>备注<input value={note} onChange={(event) => setNote(event.target.value)} placeholder={selection ? "可选：记录选择依据" : "可选"}/></label>
    {step.nodeKind === "human_confirmation" && <label>决定上下文（JSON）<textarea rows={3} value={context} onChange={(event) => setContext(event.target.value)} /></label>}
    <footer><button type="button" onClick={() => decide(false)} disabled={Boolean(busy)}>拒绝</button><button type="button" className="accept" onClick={() => decide(true)} disabled={Boolean(busy) || Boolean(selection && selectedCount === 0)}>{busy === `decide:${step.id}` ? "处理中…" : step.nodeKind === "candidate_selection" ? "导入所选候选" : step.nodeKind === "citation_selection" ? "确认所选引用" : "确认继续"}</button></footer>
  </div>;
}

const workflowAnalysisMethods = [
  { value: "overview", label: "数据概览", description: "字段类型、缺失值、唯一值和数值摘要" },
  { value: "data_quality", label: "数据质量检查", description: "重点检查缺失值、空列和字段完整性" },
  { value: "numeric_distribution", label: "数值分布概览", description: "汇总数值字段的均值、中位数和离散程度" },
];

function workflowAnalysisRequest(raw: string): { goal: string; method: string } {
  try {
    const parsed = JSON.parse(raw) as { goal?: unknown; method?: unknown };
    return { goal: typeof parsed.goal === "string" ? parsed.goal : "", method: typeof parsed.method === "string" ? parsed.method : "overview" };
  } catch {
    return { goal: "", method: "overview" };
  }
}

function workflowInputFilePath(raw: string): string {
  try {
    const values = JSON.parse(raw) as unknown;
    if (typeof values === "string") return values;
    return Array.isArray(values) && typeof values[0] === "string" ? values[0] : "";
  } catch {
	return raw.trim();
  }
}

function WorkflowInputFields({ definition, values, disabled, compact = false, chooseFile, update }: {
  definition: WorkflowDefinition;
  values: Record<string, string>;
  disabled: boolean;
  compact?: boolean;
  chooseFile: (port: WorkflowPort) => void;
  update: (name: string, value: string) => void;
}) {
  return <>{definition.inputs.map((port) => {
    const raw = values[port.name] ?? "";
    if (port.fileKind) {
      const path = workflowInputFilePath(raw);
      return <label key={port.name} className="workflow-data-input"><span>{port.description || "分析数据"}{port.required && <em>必填</em>}</span><div><button type="button" disabled={disabled} onClick={() => chooseFile(port)}><Icon name="folder" size={14}/>{path ? "更换文件" : "选择数据文件"}</button><code className={path ? "selected" : ""} title={path}>{path || "尚未选择项目数据"}</code></div></label>;
    }
    if (port.control === "analysis_request") {
      const request = workflowAnalysisRequest(raw);
      const write = (next: Partial<typeof request>) => update(port.name, JSON.stringify({ ...request, ...next }));
      return <div className="workflow-analysis-request" key={port.name}><label><span>这次希望解决什么问题？<em>必填</em></span><textarea rows={compact ? 2 : 3} disabled={disabled} value={request.goal} placeholder="例如：了解各字段的数据质量，并比较主要数值指标的分布" onChange={(event) => write({ goal: event.target.value })}/></label><label><span>分析方法</span><select disabled={disabled} value={request.method} onChange={(event) => write({ method: event.target.value })}>{workflowAnalysisMethods.map((method) => <option value={method.value} key={method.value}>{method.label} · {method.description}</option>)}</select></label></div>;
    }
    return <label key={port.name}><span>{port.description || port.name}{port.required && <em>{compact ? "必填" : "必填"}</em>}</span>{port.type === "boolean" ? <select disabled={disabled} value={raw || "false"} onChange={(event) => update(port.name, event.target.value)}><option value="false">否</option><option value="true">是</option></select> : <textarea disabled={disabled} rows={compact ? (["object", "array", "artifacts", "citations"].includes(port.type) ? 3 : 1) : (["object", "array", "artifacts", "citations"].includes(port.type) ? 3 : 2)} value={raw} placeholder={port.description || "请填写本次任务的研究目标"} onChange={(event) => update(port.name, event.target.value)}/>}</label>;
  })}</>;
}

function WorkflowStudio({ project, pythonDialogOpen, openPython, openArtifacts }: { project: Project; pythonDialogOpen: boolean; openPython: () => void; openArtifacts: () => void }) {
  const [workflowTemplates, setWorkflowTemplates] = useState<WorkflowTemplate[]>([]);
  const [selectedTemplateId, setSelectedTemplateId] = useState("");
  const [selectedTemplateDefinition, setSelectedTemplateDefinition] = useState<WorkflowDefinition | null>(null);
  const [workflows, setWorkflows] = useState<ResearchWorkflow[]>([]);
  const [detail, setDetail] = useState<WorkflowDetail | null>(null);
  const [selectedId, setSelectedId] = useState("");
  const [selectedVersionId, setSelectedVersionId] = useState("");
  const [editor, setEditor] = useState(() => JSON.stringify(emptyWorkflowTemplate.definition, null, 2));
  const [preview, setPreview] = useState<WorkflowPreview | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [feedback, setFeedback] = useState("");
  const [view, setView] = useState<"guide" | "runs" | "advanced">("guide");
  const [runs, setRuns] = useState<WorkflowRun[]>([]);
  const [selectedRunId, setSelectedRunId] = useState("");
  const [runDetail, setRunDetail] = useState<WorkflowRunDetail | null>(null);
  const [runInputs, setRunInputs] = useState<Record<string, string>>({});
  const [decisionNote, setDecisionNote] = useState("");
  const [decisionContext, setDecisionContext] = useState("{}");
  const [selectedCandidateIds, setSelectedCandidateIds] = useState<string[]>([]);
  const [selectedCitationKeys, setSelectedCitationKeys] = useState<string[]>([]);
  const [confirmRetry, setConfirmRetry] = useState(false);
  const [runPermissionMode, setRunPermissionMode] = useState<PermissionMode>("full_access");
  const [pythonPreflight, setPythonPreflight] = useState<"checking" | "ready" | "available" | "missing" | "error">("checking");
  const editorTouched = useRef(false);
  const guideRef = useRef<HTMLDivElement>(null);
  const startPanelRef = useRef<HTMLElement>(null);
  const activeWorkflowIdRef = useRef("");
  const activeRunIdRef = useRef("");
  const mutationRef = useRef("");
  const pythonRequestRef = useRef(0);
  const listRequestRef = useRef(0);
  const workflowRequestRef = useRef(0);
  const runsRequestRef = useRef(0);
  const runRequestRef = useRef(0);
  const pendingDecisionStep = runDetail?.steps.find((step) => step.status === "waiting_human_confirmation");
  const currentVersion = detail?.versions.find((value) => value.id === detail.workflow.currentVersionId) ?? detail?.versions[0];
	const currentDefinition = useMemo(() => effectiveWorkflowDefinition(currentVersion), [currentVersion]);
  const guideDefinition = useMemo(() => {
	if (detail) return currentDefinition;
    if (selectedTemplateDefinition) return selectedTemplateDefinition;
    try {
      const parsed = JSON.parse(editor) as WorkflowDefinition;
      return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : null;
    } catch {
      return null;
    }
  }, [detail, currentDefinition, editor, selectedTemplateDefinition]);

  useEffect(() => {
    setDecisionNote("");
    setDecisionContext("{}");
    setSelectedCandidateIds([]);
    setSelectedCitationKeys([]);
  }, [pendingDecisionStep?.id, pendingDecisionStep?.nodeKind]);

  const parseDefinition = useCallback((): WorkflowDefinition => {
    if (selectedTemplateDefinition) return selectedTemplateDefinition;
    const parsed = JSON.parse(editor) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("Workflow 定义必须是 JSON 对象。");
    return parsed as WorkflowDefinition;
  }, [editor, selectedTemplateDefinition]);

  const loadList = useCallback(async (nextSelection?: string) => {
	const request = ++listRequestRef.current;
    setLoading(true);
    try {
      const values = await backend<ResearchWorkflow[]>("WorkflowFacade", "List", project.id);
	  if (request !== listRequestRef.current) return;
      setWorkflows(values ?? []);
	  const target = nextSelection ?? activeWorkflowIdRef.current;
      if (target && !(values ?? []).some((value) => value.id === target)) {
		activeWorkflowIdRef.current = "";
        setSelectedId("");
        setDetail(null);
      }
    } catch (error) {
	  if (request === listRequestRef.current) setFeedback(errorText(error));
    } finally {
	  if (request === listRequestRef.current) setLoading(false);
    }
  }, [project.id]);

  useEffect(() => { void loadList(); }, [project.id]);

  const refreshPythonPreflight = useCallback(async () => {
	const request = ++pythonRequestRef.current;
    setPythonPreflight("checking");
    try {
      const [environment, discovery] = await Promise.all([
        backend<PythonEnvironment>("PythonFacade", "GetProjectEnvironment", project.id),
        backend<PythonDiscovery>("PythonFacade", "DetectInterpreters"),
      ]);
	  if (request !== pythonRequestRef.current) return;
	  if (environment.state === "ready") setPythonPreflight("ready");
      else if (discovery.interpreters.some((item) => item.hasVenv && item.prefix.toLocaleLowerCase() === item.basePrefix.toLocaleLowerCase())) setPythonPreflight("available");
      else setPythonPreflight("missing");
    } catch {
	  if (request === pythonRequestRef.current) setPythonPreflight("error");
    }
  }, [project.id]);

  useEffect(() => { void refreshPythonPreflight(); }, [refreshPythonPreflight, detail?.workflow.currentVersionId, pythonDialogOpen]);

  useEffect(() => {
    let active = true;
    void backend<WorkflowTemplate[]>("WorkflowFacade", "Templates").then((values) => {
      if (!active) return;
      const loaded = values ?? [];
      setWorkflowTemplates(loaded);
      if (!editorTouched.current && !selectedId && loaded[0]) {
        setSelectedTemplateId(loaded[0].id);
        setSelectedTemplateDefinition(loaded[0].definition);
        setEditor(JSON.stringify(loaded[0].definition, null, 2));
        setRunInputs(Object.fromEntries(loaded[0].definition.inputs.map((port) => [port.name, workflowDefaultInput(port)])));
      }
    }).catch((error) => {
      if (active) setFeedback(`参考模板加载失败：${errorText(error)}`);
    });
    return () => { active = false; };
  }, [project.id]);

  const loadRuns = useCallback(async (workflowId: string, nextRunId?: string) => {
	const request = ++runsRequestRef.current;
	if (!workflowId) { setRuns([]); setRunDetail(null); setSelectedRunId(""); return; }
	try {
	  const values = await backend<WorkflowRun[]>("WorkflowFacade", "ListRuns", project.id, workflowId, 100);
	  if (request !== runsRequestRef.current || activeWorkflowIdRef.current !== workflowId) return;
	  setRuns(values ?? []);
      const target = nextRunId ?? selectedRunId;
      if (target && !(values ?? []).some((value) => value.id === target)) {
		activeRunIdRef.current = "";
        setSelectedRunId("");
        setRunDetail(null);
      }
    } catch (error) {
	  if (request === runsRequestRef.current && activeWorkflowIdRef.current === workflowId) setFeedback(errorText(error));
    }
  }, [project.id, selectedRunId]);

  const loadRun = useCallback(async (runId: string, quiet = false) => {
	if (!runId) return;
	if (quiet && activeRunIdRef.current !== runId) return;
	if (!quiet) activeRunIdRef.current = runId;
	const workflowId = activeWorkflowIdRef.current;
	const request = ++runRequestRef.current;
	if (!quiet) setBusy("load-run");
	try {
	  const loaded = await backend<WorkflowRunDetail>("WorkflowFacade", "GetRun", project.id, runId);
	  if (request !== runRequestRef.current || !workflowId || activeWorkflowIdRef.current !== workflowId || loaded.run.workflowId !== workflowId) return;
	  setSelectedRunId(runId);
      setRunDetail(loaded);
      setRuns((current) => current.map((value) => value.id === loaded.run.id ? loaded.run : value));
    } catch (error) {
	  if (!quiet && request === runRequestRef.current && activeWorkflowIdRef.current === workflowId) setFeedback(errorText(error));
    } finally {
	  if (!quiet && request === runRequestRef.current && activeWorkflowIdRef.current === workflowId) setBusy("");
    }
  }, [project.id]);

  useEffect(() => {
    if (!runDetail || (runDetail.run.status !== "queued" && runDetail.run.status !== "running")) return;
    const timer = window.setInterval(() => void loadRun(runDetail.run.id, true), 700);
    return () => window.clearInterval(timer);
  }, [runDetail?.run.id, runDetail?.run.status, loadRun]);

  async function selectWorkflow(value: ResearchWorkflow) {
	if (busy || mutationRef.current) return;
	const request = ++workflowRequestRef.current;
	activeWorkflowIdRef.current = value.id;
	++runsRequestRef.current;
	++runRequestRef.current;
	activeRunIdRef.current = "";
	editorTouched.current = true;
    setSelectedTemplateId("");
    setSelectedTemplateDefinition(null);
    setSelectedId(value.id);
    setSelectedRunId("");
    setRunDetail(null);
    setRuns([]);
    setPreview(null);
    setFeedback("");
    setView("guide");
	try {
	  const loaded = await backend<WorkflowDetail>("WorkflowFacade", "Get", project.id, value.id);
	  if (request !== workflowRequestRef.current || activeWorkflowIdRef.current !== value.id) return;
	  setDetail(loaded);
      const current = loaded.versions.find((version) => version.id === loaded.workflow.currentVersionId) ?? loaded.versions[0];
      if (current) {
        setSelectedVersionId(current.id);
        setEditor(JSON.stringify(current.definition, null, 2));
        setRunInputs(Object.fromEntries(current.definition.inputs.map((port) => [port.name, workflowDefaultInput(port)])));
      }
      await loadRuns(value.id, "");
    } catch (error) {
	  if (request === workflowRequestRef.current && activeWorkflowIdRef.current === value.id) setFeedback(errorText(error));
    }
  }

  function newFromTemplate(template?: WorkflowTemplate) {
	if (busy || mutationRef.current) return;
	const selected = template ?? workflowTemplates[0] ?? emptyWorkflowTemplate;
	++workflowRequestRef.current;
	activeWorkflowIdRef.current = "";
	++runsRequestRef.current;
	++runRequestRef.current;
	activeRunIdRef.current = "";
    editorTouched.current = true;
    setSelectedTemplateId(selected.id);
    setSelectedTemplateDefinition(selected.definition);
    setSelectedId("");
    setDetail(null);
    setPreview(null);
    setFeedback(`已切换到“${selected.name}”。先查看研究步骤，再点击“采用此研究方案”；选择方案本身不会立即运行。`);
    setView("guide");
    setSelectedVersionId("");
    setRuns([]);
    setSelectedRunId("");
    setRunDetail(null);
    setRunInputs(Object.fromEntries(selected.definition.inputs.map((port) => [port.name, workflowDefaultInput(port)])));
    setEditor(JSON.stringify(selected.definition, null, 2));
    window.requestAnimationFrame(() => guideRef.current?.scrollTo({ top: 0, behavior: "smooth" }));
  }

  async function validate(): Promise<WorkflowPreview | null> {
    setBusy("validate");
    setFeedback("");
    try {
      const result = await backend<WorkflowPreview>("WorkflowFacade", "Validate", project.id, parseDefinition());
      setPreview(result);
      setFeedback(result.valid ? "研究方案检查通过，可以保存。" : `研究方案中有 ${result.diagnostics.filter((item) => item.severity === "error").length} 个问题需要处理。`);
      return result;
    } catch (error) {
      setPreview(null);
      setFeedback(errorText(error));
      return null;
    } finally {
      setBusy("");
    }
  }

  async function save() {
	if (busy || mutationRef.current) return;
	mutationRef.current = "save";
	setBusy("save");
    setFeedback("");
    try {
      const definition = parseDefinition();
      const checked = await backend<WorkflowPreview>("WorkflowFacade", "Validate", project.id, definition);
      setPreview(checked);
      if (!checked.valid) {
        setFeedback("研究方案检查未通过，尚未保存。");
        return;
      }
	  const saved = await backend<WorkflowSaveResult>("WorkflowFacade", "Save", {
        projectId: project.id,
        workflowId: detail?.workflow.id ?? "",
        expectedCurrentVersionId: detail?.workflow.currentVersionId ?? "",
        definition,
	  });
	  activeWorkflowIdRef.current = saved.workflow.id;
	  ++workflowRequestRef.current;
	  ++runsRequestRef.current;
	  ++runRequestRef.current;
	  setSelectedId(saved.workflow.id);
      const loaded = await backend<WorkflowDetail>("WorkflowFacade", "Get", project.id, saved.workflow.id);
      setDetail(loaded);
      setSelectedVersionId(saved.workflow.currentVersionId);
      await loadList(saved.workflow.id);
      await loadRuns(saved.workflow.id);
      setFeedback(saved.created ? "研究方案已保存，现在可以填写目标并开始任务。" : saved.version.id === detail?.workflow.currentVersionId ? "研究方案没有变化，继续使用当前版本。" : `研究方案已保存为 v${saved.version.version}。`);
      window.requestAnimationFrame(() => startPanelRef.current?.scrollIntoView({ behavior: "smooth", block: "center" }));
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
	  if (mutationRef.current === "save") mutationRef.current = "";
      setBusy("");
    }
  }

  async function deleteWorkflow(value: ResearchWorkflow) {
	if (busy || mutationRef.current) return;
	const knownHistory = value.id === detail?.workflow.id ? runs.length : 0;
	const historyText = knownHistory ? `\n\n同时会删除当前读取到的 ${knownHistory} 条任务记录及其运行日志。` : "\n\n同时会删除该方案的版本和全部任务历史。";
	if (!window.confirm(`删除研究方案“${value.name}”？${historyText}\n此操作无法撤销。`)) return;
	const operation = `delete:${value.id}`;
	mutationRef.current = operation;
	setBusy(operation);
	setFeedback("");
	try {
	  await backend<void>("WorkflowFacade", "Delete", project.id, value.id);
	  if (activeWorkflowIdRef.current === value.id) {
		++workflowRequestRef.current; ++runsRequestRef.current; ++runRequestRef.current;
		activeWorkflowIdRef.current = ""; activeRunIdRef.current = "";
		setSelectedId(""); setDetail(null); setRuns([]); setSelectedRunId(""); setRunDetail(null);
		const initial = workflowTemplates[0] ?? emptyWorkflowTemplate;
		setSelectedTemplateId(initial.id); setSelectedTemplateDefinition(initial.definition);
		setEditor(JSON.stringify(initial.definition, null, 2)); setPreview(null); setSelectedVersionId("");
		setRunInputs(Object.fromEntries(initial.definition.inputs.map((port) => [port.name, workflowDefaultInput(port)])));
	  }
	  await loadList("");
	  setFeedback(`已删除研究方案“${value.name}”及其任务历史。`);
	} catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  function loadVersion(versionId: string) {
	if (busy) return;
    const version = detail?.versions.find((value) => value.id === versionId);
    if (!version) return;
    setSelectedVersionId(version.id);
    setSelectedTemplateDefinition(null);
    setEditor(JSON.stringify(version.definition, null, 2));
    setPreview(null);
    setFeedback(version.id === detail?.workflow.currentVersionId ? `已载入当前版本 v${version.version}。` : `已载入历史版本 v${version.version} 作为编辑草稿；保存会基于当前版本创建新版本。`);
  }

  function parseRunInputs(): Record<string, unknown> {
    if (!currentVersion) throw new Error("请先选择并保存一个 Workflow。");
    const result: Record<string, unknown> = {};
    for (const port of currentVersion.definition.inputs) {
      const raw = (runInputs[port.name] ?? "").trim();
      if (!raw) {
        if (port.default !== undefined) result[port.name] = port.default;
        else if (port.required) throw new Error(`请输入 ${port.name}。`);
        continue;
      }
      if (port.type === "string") result[port.name] = raw;
      else if (port.type === "integer") {
        const parsed = Number(raw); if (!Number.isInteger(parsed)) throw new Error(`${port.name} 必须是整数。`); result[port.name] = parsed;
      } else if (port.type === "number") {
        const parsed = Number(raw); if (!Number.isFinite(parsed)) throw new Error(`${port.name} 必须是数字。`); result[port.name] = parsed;
      } else if (port.type === "boolean") result[port.name] = raw === "true";
      else {
        try { result[port.name] = JSON.parse(raw); } catch { throw new Error(`${port.name} 必须是有效 JSON。`); }
      }
    }
    return result;
  }

  function updateRunInput(name: string, value: string) {
    setRunInputs((current) => ({ ...current, [name]: value }));
    setFeedback("");
  }

  async function chooseInputFile(port: WorkflowPort) {
	const kind = port.fileKind ?? "tabular";
	const operation = `input-file:${port.name}`;
	if (busy || mutationRef.current) return;
	mutationRef.current = operation;
    setBusy(operation);
    setFeedback("");
    try {
      const selected = await backend<WorkflowInputFile>("WorkflowFacade", "ChooseInputFile", project.id, kind);
      if (!selected?.relativePath) return;
	  updateRunInput(port.name, port.type === "string" ? selected.relativePath : JSON.stringify([selected.relativePath]));
      setFeedback(`${selected.staged ? "外部文件已复制到项目" : "已选择项目文件"}：${selected.relativePath} · ${fileSize(selected.sizeBytes)} · SHA256 ${selected.sha256.slice(0, 12)}…`);
    } catch (error) {
      setFeedback(errorText(error));
    } finally {
	  if (mutationRef.current === operation) mutationRef.current = "";
      setBusy("");
    }
  }

	const inputDefinition = currentDefinition;
  const needsPython = Boolean(inputDefinition?.nodes.some((node) => node.kind === "python" || node.toolName === "builtin.research.workflow.python.ensure"));
  const inputReady = useMemo(() => {
    if (!inputDefinition) return false;
    try {
      const parsed = parseRunInputs();
      return inputDefinition.inputs.every((port) => {
		const value = parsed[port.name];
		if (port.required && value === undefined) return false;
		if (port.required && (typeof value === "string" && !value.trim() || Array.isArray(value) && value.length === 0 || value && typeof value === "object" && !Array.isArray(value) && Object.keys(value).length === 0)) return false;
		if (port.fileKind) {
		  const paths = typeof value === "string" ? [value] : Array.isArray(value) ? value : [];
		  if (!paths.length || paths.some((path) => typeof path !== "string" || !path.trim())) return false;
		  if (port.minItems && paths.length < port.minItems) return false;
		  if (port.maxItems && paths.length > port.maxItems) return false;
		}
		if (port.control === "analysis_request") {
		  const request = value as { goal?: unknown; method?: unknown };
          return typeof request?.goal === "string" && Boolean(request.goal.trim()) && workflowAnalysisMethods.some((method) => method.value === request.method);
        }
        return true;
      });
    } catch {
      return false;
    }
  }, [inputDefinition, runInputs]);

  async function startRun() {
	if (!detail || !currentVersion || busy || mutationRef.current) return;
	mutationRef.current = "start-run";
	setBusy("start-run"); setFeedback("");
    try {
      const started = await backend<WorkflowRunDetail>("WorkflowFacade", "Start", {
        projectId: project.id, workflowId: detail.workflow.id, workflowVersionId: currentVersion.id, inputs: parseRunInputs(), permissionMode: runPermissionMode,
      });
	  activeRunIdRef.current = started.run.id;
	  setRunDetail(started); setSelectedRunId(started.run.id); setView("runs");
      await loadRuns(detail.workflow.id, started.run.id);
      setFeedback("科研任务已经开始，后续阶段会依次推进。");
    } catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === "start-run") mutationRef.current = ""; setBusy(""); }
  }

  async function runAction(action: "Pause" | "Resume" | "Cancel") {
	if (!runDetail || busy || mutationRef.current) return;
	const operation = action.toLowerCase();
	mutationRef.current = operation;
	setBusy(operation); setFeedback("");
    try {
      const updated = await backend<WorkflowRunDetail>("WorkflowFacade", action, project.id, runDetail.run.id);
      setRunDetail(updated); await loadRuns(updated.run.workflowId, updated.run.id);
      setFeedback(action === "Pause" ? "任务已在当前检查点暂停。" : action === "Resume" ? "科研任务已继续。" : "已请求取消科研任务。");
    } catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  async function decide(step: WorkflowStep, approved: boolean) {
	if (!runDetail || busy || mutationRef.current) return;
	const operation = `decide:${step.id}`;
	mutationRef.current = operation;
	setBusy(operation); setFeedback("");
    try {
      let context: unknown = {};
      if (step.nodeKind === "candidate_selection") context = { selectedCandidateIds };
      else if (step.nodeKind === "citation_selection") {
        const selected = new Set(selectedCitationKeys);
        context = workflowCitations(step).filter((value) => selected.has(JSON.stringify(value)));
      } else if (decisionContext.trim()) {
        try { context = JSON.parse(decisionContext); } catch { throw new Error("人工决定上下文必须是有效 JSON。"); }
      }
      const updated = await backend<WorkflowRunDetail>("WorkflowFacade", "Decide", {
        projectId: project.id, runId: runDetail.run.id, stepId: step.id, approved, note: decisionNote, context,
      });
      setRunDetail(updated);
      await loadRuns(updated.run.workflowId, updated.run.id);
    } catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  async function resolveWorkflowApproval(approval: Approval, allow: boolean) {
	if (busy || mutationRef.current) return;
	const operation = `approval:${approval.id}`;
	mutationRef.current = operation;
	setBusy(operation); setFeedback("");
    try {
      const updated = await backend<WorkflowRunDetail>("WorkflowFacade", "ResolveApproval", { approvalId: approval.id, allow, scope: "call" });
      setRunDetail(updated); await loadRuns(updated.run.workflowId, updated.run.id);
    } catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  async function retryStep(step: WorkflowStep) {
	if (!runDetail || busy || mutationRef.current) return;
    const node = runDetail.run.compilation.nodes.find((value) => value.id === step.nodeId);
    if (node?.sideEffect && !confirmRetry) { setFeedback("该步骤可能有副作用，请先确认重新执行。"); return; }
	const operation = `retry:${step.id}`;
	mutationRef.current = operation;
	setBusy(operation); setFeedback("");
    try {
      const updated = await backend<WorkflowRunDetail>("WorkflowFacade", "Retry", {
        projectId: project.id, runId: runDetail.run.id, stepId: step.id, confirmSideEffect: Boolean(node?.sideEffect && confirmRetry), note: node?.sideEffect ? "用户在科研模式中确认重新执行可能产生副作用的步骤" : "",
      });
      setRunDetail(updated); setConfirmRetry(false); await loadRuns(updated.run.workflowId, updated.run.id);
    } catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  return <section className="research-mode-workspace" aria-label={`${project.name} 科研模式`}>
    <header className="research-mode-hero">
      <div className="research-mode-mark" aria-hidden="true"><span/><i/><Icon name="spark" size={23}/></div>
      <div><p>SCIAIDE RESEARCH MODE</p><h1>科研模式</h1><small>把研究目标转化为可跟随、可暂停、可核验的研究任务</small></div>
    </header>
    <div className="workflow-layout">
      <aside className="workflow-side">
        <header><div><p>RESEARCH PLANS</p><h2>研究方案</h2></div><button type="button" title="刷新研究方案" aria-label="刷新研究方案" disabled={Boolean(busy) || loading} onClick={() => void loadList()}><Icon name="refresh" size={14}/></button></header>
        <p className="workflow-side-intro">选择一种成果目标，SciAide 会展示完整阶段，并在需要你判断时暂停。</p>
        <div className="workflow-template-list">{workflowTemplates.map((template) => <button type="button" key={template.id} disabled={Boolean(busy)} aria-pressed={!selectedId && selectedTemplateId === template.id} className={!selectedId && selectedTemplateId === template.id ? "selected" : ""} onClick={() => newFromTemplate(template)}><span><b>{template.name}</b><small>{template.description}</small></span><Icon name={!selectedId && selectedTemplateId === template.id ? "check" : "play"} size={14}/></button>)}</div>
        <div className="workflow-saved-heading"><span>已保存的方案</span><button type="button" disabled={Boolean(busy)} onClick={() => newFromTemplate()} title="创建自定义研究方案" aria-label="创建自定义研究方案"><Icon name="plus" size={13}/></button></div>
        <div className="workflow-list">{loading ? <p>正在读取研究方案…</p> : workflows.length ? workflows.map((value) => <article key={value.id} className={selectedId === value.id ? "selected" : ""}><button type="button" disabled={Boolean(busy)} onClick={() => void selectWorkflow(value)}><span><b>{value.name}</b><small>{value.description || "无说明"}</small></span><em>v{value.version}</em></button><button type="button" className="workflow-delete" disabled={Boolean(busy)} aria-label={`删除研究方案 ${value.name}`} title="删除方案及任务历史" onClick={() => void deleteWorkflow(value)}><Icon name={busy === `delete:${value.id}` ? "refresh" : "trash"} size={13}/></button></article>) : <p>采用的研究方案会保存在这里。</p>}</div>
      </aside>
      <main className={`workflow-editor ${view === "runs" ? "runtime-view" : ""}`}>
        <div className="workflow-toolbar"><div><b>{detail?.workflow.name ?? guideDefinition?.name ?? "选择研究方案"}</b><small>{detail ? `研究方案 v${detail.workflow.version} · 每次执行都会保留独立记录` : "先了解研究阶段，再决定是否开始"}</small></div><div className="workflow-view-tabs"><button type="button" className={view === "guide" ? "selected" : ""} onClick={() => setView("guide")}>研究方案</button><button type="button" className={view === "runs" ? "selected" : ""} disabled={!detail} onClick={() => setView("runs")}>任务进度</button><button type="button" className={view === "advanced" ? "selected" : ""} onClick={() => setView("advanced")}>高级设置</button></div>{detail && view === "advanced" && <label>历史版本<select disabled={Boolean(busy)} value={selectedVersionId || detail.workflow.currentVersionId} onChange={(event) => loadVersion(event.target.value)}>{detail.versions.map((version) => <option key={version.id} value={version.id}>v{version.version} · {new Date(version.createdAt).toLocaleString()}</option>)}</select></label>}</div>
        {view === "guide" ? <div className="research-guide" ref={guideRef}>
          {guideDefinition ? <>
            <section className="research-guide-overview"><div><p>YOUR RESEARCH JOURNEY</p><h2>{guideDefinition.name}</h2><span>{guideDefinition.description || "按阶段推进研究，并为关键结果保留来源记录。"}</span></div><aside><b>{guideDefinition.nodes.length}</b><small>个研究阶段</small></aside></section>
            {!detail && selectedTemplateId && <div className="research-template-notice"><Icon name="check" size={16}/><span><b>已选择“{guideDefinition.name}”</b><small>当前只是在预览研究步骤；点击下方“采用此研究方案”后，才可以填写输入并启动任务。</small></span></div>}
            <section className="research-stage-map"><header><div><b>研究将如何进行</b><small>程序会自动完成可执行步骤，需要判断时会停下来邀请你选择。</small></div><span>{runDetail ? `${runDetail.steps.filter((step) => step.status === "completed").length}/${runDetail.steps.length} 已完成` : "开始前预览"}</span></header><div>{guideDefinition.nodes.map((node, index) => { const step = runDetail?.steps.find((value) => value.nodeId === node.id); const interactive = ["human_confirmation", "candidate_selection", "citation_selection"].includes(node.kind); return <article key={node.id} className={step?.status || (index === 0 ? "next" : "pending")}><i>{step?.status === "completed" ? <Icon name="check" size={13}/> : index + 1}</i><span><b>{node.name}</b><small>{node.prompt || (interactive ? "这一步需要你查看结果并做出选择。" : node.kind === "python" ? "SciAide 将在项目 Python 环境中执行可复现分析。" : "SciAide 自动执行，并保存本阶段的输入与结果。")}</small></span><em>{step ? workflowStepLabels[step.status] : interactive ? "需要参与" : "自动进行"}</em></article>; })}</div></section>
            <section className="research-start-panel" ref={startPanelRef}><header><div><b>{detail ? "配置并开始科研任务" : "采用这套研究方案"}</b><small>{detail ? "输入、方案版本与工具权限会在任务启动时冻结；人工筛选仍会停下来等待你。" : "保存后将自动进入任务配置，不会因为采用方案而立即运行。"}</small></div>{detail && <span>方案 v{detail.workflow.version}</span>}</header>{detail ? <div className="research-guide-inputs"><label className={`workflow-permission-mode ${runPermissionMode}`}><span><Icon name="shield" size={15}/><div><b>任务工具权限</b><small>{runPermissionMode === "full_access" ? "默认放开已注册工具，仍受 Workspace 路径、Schema 和执行边界保护" : "写入、进程与高风险工具会逐次请求确认"}</small></div></span><select value={runPermissionMode} disabled={Boolean(busy)} onChange={(event) => setRunPermissionMode(event.target.value as PermissionMode)}><option value="full_access">Full Access · 自动执行</option><option value="plan">Plan · 逐次确认</option></select></label>{needsPython && <div className={`workflow-python-preflight ${pythonPreflight}`}><Icon name={pythonPreflight === "ready" || pythonPreflight === "available" ? "check" : pythonPreflight === "checking" ? "refresh" : "tool"} size={15}/><span><b>{pythonPreflight === "ready" ? "项目 Python 运行环境已就绪" : pythonPreflight === "available" ? "启动后将自动创建项目 Python 运行环境" : pythonPreflight === "missing" ? "未检测到可用的 Python 3" : pythonPreflight === "error" ? "无法读取 Python 环境状态" : "正在检查 Python 环境"}</b><small>{pythonPreflight === "missing" ? "请先安装 Python 3，或在 Python 环境中绑定已有虚拟环境。" : "系统 Python 只负责创建；分析工具使用项目隔离环境，代码、依赖和指纹会进入复现记录。"}</small></span><button type="button" onClick={openPython}>{pythonPreflight === "missing" ? "设置环境" : "查看环境"}</button></div>}{inputDefinition?.inputs.length ? <WorkflowInputFields definition={inputDefinition} values={runInputs} disabled={Boolean(busy)} chooseFile={(port) => void chooseInputFile(port)} update={updateRunInput}/> : <p>这套研究方案无需额外输入，可以直接开始。</p>}<div className="research-launch-row"><span className={inputReady ? "ready" : "waiting"}><Icon name={inputReady ? "check" : "history"} size={14}/>{inputReady ? `任务输入已就绪 · ${runPermissionMode === "full_access" ? "Full Access" : "Plan"}` : "请先完成必填输入"}</span><button type="button" disabled={!currentVersion || !inputReady || needsPython && (pythonPreflight === "missing" || pythonPreflight === "checking") || Boolean(busy)} onClick={() => void startRun()}><Icon name="play" size={15}/>{busy === "start-run" ? "正在建立科研任务…" : "开始科研任务"}</button></div></div> : <button type="button" className="research-adopt-plan" disabled={Boolean(busy)} onClick={() => void save()}><Icon name="check" size={15}/>{busy === "save" ? "正在检查方案…" : "采用此研究方案"}</button>}</section>
          </> : <div className="workflow-run-empty"><Icon name="history" size={27}/><b>请选择一种研究方案</b><span>从左侧选择你想完成的成果类型，完整研究步骤会显示在这里。</span></div>}
          {feedback && <div className="research-guide-feedback"><Icon name="shield" size={13}/><span>{feedback}</span></div>}
        </div> : view === "advanced" ? <>
          <div className="workflow-definition"><label>Workflow JSON<textarea spellCheck={false} value={editor} onChange={(event) => { setSelectedTemplateDefinition(null); setEditor(event.target.value); setPreview(null); setFeedback(""); }} /></label></div>
          <section className="workflow-preview">
            <header><div><b>执行前预览</b><small>{preview ? `${preview.nodes.length} 节点 · ${preview.edgeCount} 边 · ${preview.valid ? "可保存" : "不可保存"}` : "校验后显示冻结工具、顺序、权限与风险"}</small></div><span className={preview?.valid ? "valid" : preview ? "invalid" : "idle"}>{preview?.valid ? "通过" : preview ? "有错误" : "未校验"}</span></header>
            {preview?.diagnostics.length ? <div className="workflow-diagnostics">{preview.diagnostics.map((item, index) => <div className={item.severity} key={`${item.code}:${item.path}:${index}`}><code>{item.code}</code><span><b>{item.path}</b>{item.message}</span></div>)}</div> : preview?.nodes.length ? <div className="workflow-order">{preview.nodes.map((node) => <article key={node.id}><i>{node.ordinal}</i><div><b>{node.name}</b><code>{node.toolName ? `${node.toolName}@${node.toolVersion}` : node.kind}</code><small>{node.summary}</small></div><span className={`risk-${node.risk || "none"}`}>{node.risk || (node.kind === "human_confirmation" ? "人工确认" : "只读选择")}</span><footer>{node.permissions.length ? node.permissions.map((permission) => <em key={`${permission.kind}:${permission.resource}`}>{permission.kind}{permission.resource ? ` · ${permission.resource}` : ""}</em>) : <em>无工具权限</em>}<em>{node.idempotent ? "幂等" : node.sideEffect ? "可能有副作用" : "控制节点"}</em></footer></article>)}</div> : <div className="workflow-preview-empty"><Icon name="shield" size={21}/><span>模板、导入定义和 JSON 都是不可信数据；校验不会执行任何节点。</span></div>}
          </section>
          <footer className="workflow-actions"><span>{feedback}</span><button type="button" onClick={() => void validate()} disabled={Boolean(busy)}><Icon name="shield" size={14}/>{busy === "validate" ? "校验中…" : "静态校验"}</button><button type="button" className="primary" onClick={() => void save()} disabled={Boolean(busy) || preview?.valid === false}><Icon name="check" size={14}/>{busy === "save" ? "保存中…" : detail ? "保存新版本" : "创建流程"}</button></footer>
        </> : <>
          <div className="workflow-runtime">
            <aside className="workflow-run-panel">
              <section className="workflow-run-inputs"><header><b>发起新任务</b><small>输入在任务开始后固定</small></header>{inputDefinition?.inputs.length ? <WorkflowInputFields definition={inputDefinition} values={runInputs} disabled={Boolean(busy)} compact chooseFile={(port) => void chooseInputFile(port)} update={updateRunInput}/> : <p>此研究方案无需额外输入。</p>}<button type="button" className="primary" disabled={!currentVersion || !inputReady || Boolean(busy)} onClick={() => void startRun()}><Icon name="play" size={13}/>{busy === "start-run" ? "正在启动…" : "开始科研任务"}</button></section>
              <section className="workflow-run-history"><header><b>任务记录</b><button type="button" aria-label="刷新任务记录" title="刷新" onClick={() => detail && void loadRuns(detail.workflow.id)}><Icon name="refresh" size={12}/></button></header><div>{runs.length ? runs.map((run) => <button type="button" key={run.id} className={selectedRunId === run.id ? "selected" : ""} onClick={() => void loadRun(run.id)}><span><b>{workflowRunLabels[run.status]}</b><small>{new Date(run.createdAt).toLocaleString()}</small></span><em className={run.status}>{run.currentStep}/{run.compilation.order.length}</em></button>) : <p>这套研究方案还没有任务记录。</p>}</div></section>
            </aside>
            <section className="workflow-run-detail">{runDetail ? <>
              <header className="workflow-run-summary"><div><span className={`workflow-status ${runDetail.run.status}`}>{workflowRunLabels[runDetail.run.status]}</span><b>科研任务 {runDetail.run.id.slice(0, 8)}</b><small>研究方案快照 {runDetail.run.workflowVersionId.slice(0, 8)} · 权限 {runDetail.run.permissionMode === "full_access" ? "Full Access" : "Plan"} · {new Date(runDetail.run.updatedAt).toLocaleString()}</small></div><nav>{runDetail.run.status === "paused" && <button type="button" onClick={() => void runAction("Resume")} disabled={Boolean(busy)}><Icon name="play" size={13}/>继续</button>}{["queued", "waiting_approval", "waiting_human_confirmation"].includes(runDetail.run.status) && <button type="button" onClick={() => void runAction("Pause")} disabled={Boolean(busy)}><Icon name="pause" size={13}/>暂停</button>}{!workflowTerminal(runDetail.run.status) && <button type="button" className="danger" onClick={() => void runAction("Cancel")} disabled={Boolean(busy)}><Icon name="stop" size={12}/>取消</button>}</nav></header>
              {runDetail.run.errorMessage && <div className="workflow-run-error"><code>{runDetail.run.errorCode || "WORKFLOW_FAILED"}</code><span>{runDetail.run.errorMessage}</span></div>}
              {runDetail.pendingApprovals.length > 0 && <section className="workflow-runtime-approvals"><h3>待授权</h3>{runDetail.pendingApprovals.map((approval) => <article key={approval.id}><div><b>{approval.toolName}</b><code>{approval.permissionKind}{approval.resource ? ` · ${approval.resource}` : ""}</code><p>{approval.reason}</p></div><footer><button type="button" onClick={() => void resolveWorkflowApproval(approval, false)} disabled={Boolean(busy)}>拒绝</button><button type="button" className="accept" onClick={() => void resolveWorkflowApproval(approval, true)} disabled={Boolean(busy)}>{busy === `approval:${approval.id}` ? "处理中…" : "接受本次"}</button></footer></article>)}</section>}
              <WorkflowRunEvidence detail={runDetail}/>
              <section className="workflow-step-timeline"><h3>研究进度</h3>{runDetail.steps.map((step) => { const node = runDetail.run.compilation.nodes.find((value) => value.id === step.nodeId); const retryable = ["failed", "interrupted", "outcome_unknown"].includes(step.status) && ["failed", "interrupted"].includes(runDetail.run.status); const interactive = ["human_confirmation", "candidate_selection", "citation_selection"].includes(step.nodeKind); return <article key={step.id} className={step.status}><i>{step.ordinal + 1}</i><div className="workflow-step-main"><header><div><b>{node?.name || `研究阶段 ${step.ordinal + 1}`}</b><small>{interactive ? "需要你的参与" : step.nodeKind === "python" ? "分析与复现" : "自动进行"}</small></div><span>{workflowStepLabels[step.status]}{step.attempt ? ` · 第 ${step.attempt} 次` : ""}</span></header>{step.errorMessage && <p className="step-error">{step.errorMessage}</p>}{step.status === "waiting_human_confirmation" && <WorkflowHumanDecision step={step} prompt={node?.prompt} note={decisionNote} context={decisionContext} selectedCandidateIds={selectedCandidateIds} selectedCitationKeys={selectedCitationKeys} busy={busy} setNote={setDecisionNote} setContext={setDecisionContext} setSelectedCandidateIds={setSelectedCandidateIds} setSelectedCitationKeys={setSelectedCitationKeys} decide={(approved) => void decide(step, approved)}/>} {retryable && <div className="workflow-step-retry">{node?.sideEffect && <label><input type="checkbox" checked={confirmRetry} onChange={(event) => setConfirmRetry(event.target.checked)}/>我确认此步骤可能已产生副作用，仍要重新执行</label>}<button type="button" onClick={() => void retryStep(step)} disabled={Boolean(busy) || Boolean(node?.sideEffect && !confirmRetry)}><Icon name="refresh" size={12}/>{busy === `retry:${step.id}` ? "重试中…" : "从此步骤重试"}</button></div>}<details><summary>技术记录</summary><p className="workflow-step-technical">{node?.tool?.qualifiedName || step.nodeKind}{step.errorCode ? ` · ${step.errorCode}` : ""}</p><div><label>输入<pre>{workflowJSON(step.input)}</pre></label><label>输出<pre>{workflowJSON(step.output)}</pre></label></div></details></div></article>; })}</section>
              {runDetail.run.status === "completed" && <section className="workflow-run-output"><header><div><h3>研究任务已完成</h3><small>分析文件、图表和报告已经登记到项目科研产物。</small></div><button type="button" onClick={openArtifacts}><Icon name="archive" size={13}/>查看科研产物</button></header><details><summary>查看结构化最终输出</summary><pre>{workflowJSON(runDetail.run.outputs)}</pre></details></section>}
              <details className="workflow-event-log"><summary>事件审计 · {runDetail.events.length} 条</summary>{runDetail.events.map((event) => <article key={event.id}><code>#{event.sequence} {event.type}</code><time>{new Date(event.createdAt).toLocaleString()}</time><pre>{workflowJSON(event.payload)}</pre></article>)}</details>
            </> : <div className="workflow-run-empty"><Icon name="history" size={27}/><b>选择一条科研任务记录</b><span>你可以查看每个研究阶段、等待事项、结果与完整来源链。</span></div>}</section>
          </div>
          <footer className="workflow-actions runtime"><span>{feedback}</span></footer>
        </>}
      </main>
    </div>
  </section>;
}
