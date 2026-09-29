import {RevisionLibraryPicker} from "./RevisionLibraryPicker";
import {ModalBackdrop, ModalDialog, useDialogGuard} from "./Modal";
import { NetworkSettings, BrowserEnvironment, SearchNetworkStatus, SettingsPage, SettingsDialog } from "./ApplicationSettings";
import { retryStatusLabel, applyRetryEvent, RetryDisplayStatus } from "./retryPresentation.js";
import { ResearchRevisionControls, RevisionRecommendation } from "./ResearchRevisionControls";
import { ReferenceMaterials } from "./ReferenceMaterials";
import { ResearchMaterialsLibrary, type ResearchMaterial } from "./ResearchMaterialsLibrary";
import { ResearchRevisionCards, ResearchRevisionProposal } from "./ResearchRevisionCards";
import { ResearchReviewFeedback } from "./ResearchReviewFeedback";
import { reviewTrackingModel, ReviewIssues } from "./researchReviewPresentation.js";
import { ResearchTaskTimeline } from "./ResearchTaskTimeline";
import { compareRunCreatedAt } from "./researchTimelineState.js";
import { conditionalViews, shareSnapshot, singleFlight } from "./snapshotState.js";
import { ResearchClarificationDialog, ResearchClarificationQuestion, clarificationModeLabel, clarificationSummary } from "./ResearchClarificationDialog";
import { CSSProperties, Component, ErrorInfo, FormEvent, Fragment, memo, ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import ReactMarkdown from "react-markdown";
import { MarkdownCodeBlock } from "./MarkdownCodeBlock";
import { markdownRemarkPlugins, markdownRehypePlugins } from "./markdownPlugins.js";
import "katex/dist/katex.min.css";
import { eventsOn, minimiseWindow, onFileDrop, openDefaultBrowser, quitApplication, setClipboardText, toggleMaximiseWindow } from "./lib/wailsRuntime";
import { citationReferenceFromURL, safeMarkdownURL } from "./markdownRender.js";
import { researchAnswerPresentation } from "./researchAnswer.js";
import { citationDisplayMap, citationSourceURL, citationTextSegments, reportCitationSnapshot, CitationDisplayMap, DisplayCitation } from "./citationPresentation.js";
import { createLatestRequestGate, skillTreePrefix } from "./skillSourceTree.js";
import { deriveWorkflowRunEvidence, WorkflowEvidenceNode } from "./workflowRunEvidence.js";
import { activityDurationLabel, activityTruncationLabel, safeToolArguments, toolPresentation, toolResultSummary } from "./toolActivity.js";
type Project = { id: string; name: string; description: string; workspacePath: string; workspaceKind: "managed" | "external" };
type PermissionMode = "plan" | "full_access";
type WorkspaceMode = "chat" | "research";
type ResearchSideView = "loading" | "tasks" | "plans";
type ResearchSideViewChoice = Exclude<ResearchSideView, "loading">;
type ReasoningLevel = "low" | "medium" | "high" | "xhigh" | "max";
type APIProtocol = "openai_chat_completions" | "openai_responses" | "anthropic_messages";
type Conversation = { id: string; projectId: string; title: string; modelProfileId: string; modelId: string; permissionMode: PermissionMode; reasoningLevel: ReasoningLevel };
type AttachmentReference = { attachmentId: string; originalName: string; mimeType: string; format: string; sizeBytes: number; unitCount: number; truncated: boolean };
type MessagePart = { type: string; text?: string; payload?: AttachmentReference };
type Citation = { id: string; messageId: string; runId: string; toolCallId: string; projectId: string; reference: string; ordinal: number; indexVersionId: string; documentId: string; attachmentId: string; chunkId: string; sourceName: string; mimeType?: string; locator: string; title?: string; quote: string; quoteSha256: string; sourceStart: number; sourceEnd: number; bibliographyId?: string; bibliography?: ResearchBibliographySnapshot; evidenceLevel?: ResearchEvidenceLevel; createdAt: string };
type MessageReasoning = { status: string; requestedLevel: ReasoningLevel; resolvedLevel?: ReasoningLevel; observed: boolean; signatureObserved: boolean; tokens: number; summary?: string };
type Message = { id: string; runId?: string; createdAt?: string; internal: boolean; workflowStatus?: WorkflowRunStatus; role: "user" | "assistant" | "system" | "tool"; status: string; parts: MessagePart[]; citations?: Citation[]; reasoning?: MessageReasoning };
type OutgoingMessage = {projectId: string; taskId: string; conversationId: string; message: Message};
type ResourceScope = "task" | "project_shared" | "legacy_project" | "conversation";
type ResearchTask = { id: string; projectId: string; title: string; researchQuestion: string; originKind: "ai_route" | "template"; status: "active" | "completed" | "failed" | "cancelled" | "archived"; latestRunId?: string; latestRunStatus?: string; attachmentCount: number; knowledgeCount: number; artifactCount: number; evidenceCount: number; bibliographyCount: number; createdAt: string; updatedAt: string; archivedAt?: string };
type ResourceScopeEntry = { key: string; kind: "project_shared" | "legacy_project" | "task"; taskId?: string; title: string; count: number };
type ResourceTreeColumn = { key: string; title: string; subtitle: string; items: ReactNode[] };
export type Attachment = { id: string; projectId: string; scopeKind?: ResourceScope; researchTaskId?: string; sourceKind?: "unknown" | "user_import" | "research_import" | "conversation_upload"; originalName: string; mimeType: string; format: string; sizeBytes: number; sha256: string; status: "parsing" | "ready" | "failed"; unitCount: number; extractedRunes: number; truncated: boolean; errorMessage?: string };
type AttachmentImportBatch = { attachments: Attachment[]; errors: { path: string; message: string }[] };
type ResearchSource = { id: string; name: string; domain: string; description: string; homepage: string; host: string; keyFree: boolean; fullText: boolean };
type ResearchAuthor = { name: string; orcid?: string; raw?: string; source?: string };
type ResearchIdentifiers = { doi?: string; pmid?: string; pmcid?: string; arxiv?: string; openAlex?: string; semanticScholar?: string };
type ResearchWork = { sourceId: string; sourceRecordId: string; title: string; abstract?: string; authors: ResearchAuthor[]; year?: number; published?: string; venue?: string; volume?: string; issue?: string; pages?: string; publisher?: string; workType?: string; language?: string; identifiers: ResearchIdentifiers; landingUrl?: string; pdfUrl?: string; openAccess: boolean; citedByCount?: number; score?: number };
type ResearchSourceStatus = { sourceId: string; status: "ok" | "empty" | "failed"; count: number; errorCode?: string; message?: string; retryable: boolean };
type ResearchQuery = { taskDeleted?: boolean; legacySnapshot?: boolean; researchTaskId?: string; researchTaskTitle?: string; id: string; projectId: string; text: string; sourceIds: string[]; limitPerSource: number; sources: ResearchSourceStatus[]; partial: boolean; resultCount: number; createdAt: string; updatedAt: string };
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
type ResearchArtifact = { id: string; projectId: string; scopeKind?: ResourceScope; researchTaskId?: string; name: string; kind: ArtifactKind; status: ArtifactStatus; currentVersionId: string; currentVersion?: ArtifactVersion; createdAt: string; updatedAt: string; trashedAt?: string };
type ArtifactDetail = { artifact: ResearchArtifact; versions: ArtifactVersion[] };
type ArtifactSaveResult = { artifact: ResearchArtifact; version: ArtifactVersion; created: boolean };
type ArtifactExportResult = { export: ArtifactExport; created: boolean };
type ArtifactPreviewBlock = { kind: "heading" | "paragraph" | "list_item" | "quote" | "code" | "table" | "rule"; level?: number; text?: string; locator?: string; rows?: string[][] };
type ArtifactStructuredPreview = { title?: string; format: string; blocks: ArtifactPreviewBlock[]; metadata: Record<string, string> };
type ArtifactPreview = { versionId: string; kind: "text" | "image" | "binary" | "document"; text?: string; data?: string; mimeType: string; truncated: boolean; document?: ArtifactStructuredPreview };
type ArtifactIntegrity = { artifactId: string; versionId: string; status: "verified" | "missing" | "mismatch"; expectedSize: number; actualSize: number; expectedSha256: string; actualSha256?: string; message?: string; checkedAt: string };
type ProfileModel = { id: string; ownedBy?: string; enabled: boolean; isDefault: boolean; contextWindowTokens: number; autoCompactTokenLimit: number; contextWindowSource: "fallback" | "provider" | "manual" | "builtin"; reasoningLevels: ReasoningLevel[]; reasoningCapabilitySource?: string; reasoningVerifiedLevels?: ReasoningLevel[]; reasoningRejectedLevels?: ReasoningLevel[]; reasoningControlUnsupported?: boolean; reasoningLastRequestedLevel?: ReasoningLevel; reasoningLastResolvedLevel?: ReasoningLevel; reasoningWireMode?: string };
type Profile = { id: string; name: string; apiProtocol: APIProtocol; baseUrl: string; modelId: string; models: ProfileModel[]; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; customHeaders: Record<string, string>; enabled: boolean; isDefault: boolean };
type AvailableModel = { id: string; ownedBy?: string; contextWindowTokens?: number; autoCompactTokenLimit?: number; contextWindowSource?: "fallback" | "provider" | "manual" | "builtin"; reasoningLevels?: ReasoningLevel[]; reasoningCapabilitySource?: string };
type Run = { id: string; conversationId: string; assistantMessageId: string; modelProfileId: string; modelId: string; status: string; errorCode?: string; errorMessage?: string; errorDetails?: string; apiProtocol: APIProtocol; inputTokens: number; freshInputTokens: number; outputTokens: number; reasoningTokens: number; reasoningObserved: boolean; reasoningSignatureObserved: boolean; reasoningSummary?: string; cachedInputTokens: number; cacheWriteTokens: number; cacheReportedTurns: number; cacheHitTurns: number; permissionMode: PermissionMode; requestedReasoningLevel: ReasoningLevel; resolvedReasoningLevel?: ReasoningLevel; contextWindowTokens: number; contextBudgetTokens: number; autoCompactTokenLimit: number; contextWindowSource: "fallback" | "provider" | "manual" | "builtin"; contextCompacted: boolean; createdAt: string; startedAt?: string; completedAt?: string };
type RunStep = { runId: string; turnIndex: number; commentary?: string; reasoningSummary?: string; reasoningObserved: boolean; reasoningSignatureObserved: boolean; createdAt: string; completedAt: string };
type RetryStatus = RetryDisplayStatus;
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
type RunSnapshot = { sequence: number; run: Run; workflowAI: boolean; messages: Message[]; toolCalls: ToolCall[]; runSteps: RunStep[]; pendingApprovals: Approval[] };
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
type WorkflowNodeKind = "tool" | "shell" | "python" | "human_confirmation" | "candidate_selection" | "citation_selection" | "ai_analysis" | "agent_stage";
type WorkflowPort = { name: string; type: WorkflowDataType; description?: string; fileKind?: "delimited" | "xlsx" | "tabular"; control?: "analysis_request"; minItems?: number; maxItems?: number; required: boolean; default?: unknown };
type WorkflowNode = { id: string; name: string; kind: WorkflowNodeKind; toolName?: string; arguments: Record<string, unknown>; prompt?: string; promptVersion?: string; allowedTools?: string[]; skillRouting?: boolean; reviewPolicy?: "human" | "auto"; outputSchema?: Record<string, unknown> };
type WorkflowEdge = { fromNode: string; fromPort: string; toNode: string; toPort: string };
type WorkflowOutput = { name: string; type: WorkflowDataType; fromNode: string; fromPort: string; required: boolean; description?: string };
type WorkflowDefinition = { schemaVersion: number; name: string; description?: string; inputs: WorkflowPort[]; nodes: WorkflowNode[]; edges: WorkflowEdge[]; outputs: WorkflowOutput[] };
type WorkflowTemplate = { id: string; name: string; description: string; definition: WorkflowDefinition };
type WorkflowDiagnostic = { severity: "error" | "warning"; code: string; path: string; message: string };
type WorkflowPreviewNode = { ordinal: number; id: string; name: string; kind: WorkflowNodeKind; toolName?: string; toolVersion?: string; risk?: string; permissions: PermissionRequirement[]; idempotent: boolean; sideEffect: boolean; summary: string };
type WorkflowPreview = { valid: boolean; definitionSha256?: string; compilationSha256?: string; diagnostics: WorkflowDiagnostic[]; nodes: WorkflowPreviewNode[]; edgeCount: number; inputCount: number; outputCount: number };
type WorkflowPurpose = "user_plan" | "research_starter";
type ResearchWorkflow = { id: string; projectId: string; purpose: WorkflowPurpose; name: string; description?: string; currentVersionId: string; currentDefinitionSha256?: string; version: number; createdAt: string; updatedAt: string };
type WorkflowVersion = { id: string; workflowId: string; version: number; definition: WorkflowDefinition; definitionSha256: string; compilation: WorkflowCompilation; compilationSha256: string; runtimeInputs?: WorkflowPort[]; createdAt: string };
type WorkflowDetail = { workflow: ResearchWorkflow; versions: WorkflowVersion[] };
type WorkflowSaveResult = { workflow: ResearchWorkflow; version: WorkflowVersion; created: boolean };
type WorkflowInputFile = { relativePath: string; name: string; sizeBytes: number; sha256: string; staged: boolean };
type WorkflowRunStatus = "queued" | "running" | "waiting_approval" | "waiting_human_confirmation" | "paused" | "completed" | "failed" | "cancelled" | "interrupted";
type WorkflowStepStatus = "queued" | "running" | "waiting_approval" | "waiting_human_confirmation" | "completed" | "failed" | "cancelled" | "interrupted" | "outcome_unknown";
type WorkflowToolSnapshot = { qualifiedName: string; version: string; risk: string; permissions: PermissionRequirement[]; idempotent: boolean; inputSchema: unknown; outputSchema?: unknown };
type WorkflowCompiledNode = { id: string; name: string; kind: WorkflowNodeKind; tool?: WorkflowToolSnapshot; arguments: Record<string, unknown>; prompt?: string; promptVersion?: string; allowedTools?: WorkflowToolSnapshot[]; skillRouting?: boolean; reviewPolicy?: "human" | "auto"; outputSchema?: Record<string, unknown>; outputSchemaSha256?: string; dependencies: string[]; sideEffect: boolean };
type WorkflowCompilation = { schemaVersion: number; compilerVersion: string; definitionSha256: string; compilationSha256: string; order: string[]; nodes: WorkflowCompiledNode[]; edges: WorkflowEdge[]; inputs: WorkflowPort[]; outputs: WorkflowOutput[]; diagnostics: WorkflowDiagnostic[] };
type WorkflowRun = { id: string; researchTaskId?: string; researchStarterRunId?: string; conversationId?: string; projectId: string; workflowId: string; workflowVersionId: string; workflowName: string; workflowPurpose: WorkflowPurpose; status: WorkflowRunStatus; permissionMode: PermissionMode; inputs: Record<string, unknown>; inputsSha256: string; compilation: WorkflowCompilation; compilationSha256: string; outputs: Record<string, unknown>; currentStep: number; errorCode?: string; errorMessage?: string; cancelRequested: boolean; resumeStatus?: WorkflowRunStatus; createdAt: string; startedAt?: string; completedAt?: string; updatedAt: string };
type WorkflowStep = { id: string; workflowRunId: string; nodeId: string; ordinal: number; nodeKind: WorkflowNodeKind; status: WorkflowStepStatus; attempt: number; input: Record<string, unknown>; inputSha256?: string; output: Record<string, unknown>; toolCallId?: string; idempotencyKey?: string; errorCode?: string; errorMessage?: string; startedAt?: string; completedAt?: string; updatedAt: string };
type WorkflowRuntimeEvent = { id: string; workflowRunId: string; sequence: number; type: string; payload: Record<string, unknown>; createdAt: string };
type WorkflowAIExecution = { id: string; workflowRunId: string; workflowStepId: string; chatRunId?: string; attempt: number; nodeKind: "ai_analysis" | "agent_stage"; modelProfileId: string; modelId: string; reasoningLevel: ReasoningLevel; promptVersion: string; promptSha256: string; inputSha256: string; allowedTools: string[]; outputSchema: Record<string, unknown>; outputSchemaSha256: string; status: "prepared" | "running" | "completed" | "failed" | "cancelled" | "interrupted"; outputText?: string; output: Record<string, unknown>; outputSha256?: string; inputTokens: number; outputTokens: number; reasoningTokens: number; modelTurns: number; errorCode?: string; errorMessage?: string; createdAt: string; startedAt?: string; completedAt?: string; updatedAt: string };
type AIStageToolActivity = { id: string; toolName: string; status: string; risk: string; summary: string; arguments?: unknown; permissions?: PermissionRequirement[]; outputSummary?: string; stdoutTail?: string; stderrTail?: string; stdoutBytes?: number; stderrBytes?: number; processId?: number; liveUpdatedAt?: string; errorCode?: string; errorMessage?: string; durationMillis?: number; truncated?: boolean; createdAt: string; startedAt?: string; completedAt?: string };
type WorkflowToolActivity = { id: string; stepId?: string; toolName: string; status: string; risk: string; summary: string; arguments?: unknown; permissions?: PermissionRequirement[]; outputSummary?: string; errorMessage?: string; durationMillis?: number; truncated?: boolean; createdAt: string; startedAt?: string; completedAt?: string };
type AIStageActivity = { executionId: string; workflowStepId: string; chatRunId: string; status: string; chatStatus?: string; modelId?: string; modelTurns: number; inputTokens: number; outputTokens: number; reasoningTokens: number; currentAction?: string; currentDraft?: string; elapsedSeconds: number; toolCalls: AIStageToolActivity[]; pendingApprovals?: Approval[]; lastError?: string; startedAt?: string; updatedAt: string };
type WorkflowMessageActivity = {
  stageLabel?: string;
  resultSummary?: string;
  progressLabel?: string;
  activity: AIStageActivity;
  workflowTools: WorkflowToolActivity[];
  approvals: Approval[];
  busy: string;
  resolveApproval?: (approval: Approval, allow: boolean) => void | Promise<void>;
};
type WorkflowTimelineCall = {
  call: WorkflowToolActivity;
  label: string;
  approval?: Approval;
  busy: string;
  resolveApproval?: (approval: Approval, allow: boolean) => void | Promise<void>;
};
export type ResearchTimelineEntry = {
  id: string; sequence: string; runId: string; conversationId: string; kind: string; eventType?: string; createdAt: string;
  snapshot: { event?: Record<string, unknown>; workflowPurpose?: string; workflowName?: string; nodeName?: string; prompt?: string; inputs?: Record<string, unknown>; outputs?: Record<string, unknown>; errorMessage?: string; decision?: {approved?: boolean; note?: string; context?: unknown}; name?: string; historical?: boolean };
  message?: Message; tool?: WorkflowToolActivity; proposal?: ResearchRevisionProposal; step?: WorkflowStep; active: boolean;
};
export type ResearchTimelinePage = {taskId: string; latestRunId: string; conversationId: string; entries: ResearchTimelineEntry[]; nextBefore?: string; hasMore: boolean};
type ResearchTimelineView = {projectId: string; taskId: string; render: (entry: ResearchTimelineEntry) => ReactNode};
const readResearchTimeline = (query: {projectId: string; taskId: string; before?: string; after?: string; limit: number}) => backend<ResearchTimelinePage>("WorkflowFacade", "ResearchTimeline", query);
type ResearchDeliveryAssessment = { status: "pending" | "blocked" | "revision_required" | "reviewed" | "unverified"; kind: string; label: string; summary: string; checks?: Array<{ criterionId: string; status: "met" | "not_met"; basis: string }>; limitations?: string[] };
type WorkflowRunDetail = { run: WorkflowRun; steps: WorkflowStep[]; events: WorkflowRuntimeEvent[]; pendingApprovals: Approval[]; aiExecutions: WorkflowAIExecution[]; aiActivities?: AIStageActivity[]; toolActivities?: WorkflowToolActivity[]; artifactCount: number; registeredDeliverables?: string[]; deliveryAssessment?: ResearchDeliveryAssessment; revisionProposals?: ResearchRevisionProposal[]; reviewRevisionRecommendation?: RevisionRecommendation; reviewRevisionTargets?: Array<{ nodeId: string; label: string; repeatsSideEffects: boolean }> };
type WorkflowConversationLookup = { found: boolean; run?: WorkflowRunDetail };
type ResearchStageTask = { chatRunId: string; nodeName: string; nodeKind: "ai_analysis" | "agent_stage"; promptVersion: string; status: WorkflowAIExecution["status"]; skillRouting: boolean; reviewPolicy: "human" | "auto"; workflowStatus?: WorkflowRunStatus };
type ResearchRouteStage = { stageId: string; objective: string; methods: string[]; skillNames: string[]; inputs: string[]; outputs: string[]; humanCheckpoint: boolean };
type ResearchRouteLayer = { layerId: string; title: string; objective: string; stages: ResearchRouteStage[] };
type ResearchSkillSelection = { name: string; role: string; stageIds?: string[]; limitations: string[] };
type ResearchRouteValidation = "ready" | "blocked" | "invalid" | "checking";
type ResearchStarterRoute = { routeId: string; title: string; reason: string; availableNow: boolean; validation?: ResearchRouteValidation; validationError?: string; planningNotes?: string[]; requiredResources: string[]; deliverables: string[]; blockers: string[]; stageIds: string[]; reviewCheckpoints: string[]; layers: ResearchRouteLayer[] };
type ResearchClarificationOption = { id: string; label: string };
type ResearchClarification = { needsUserInput: boolean; intro?: string; questions: ResearchClarificationQuestion[] };
type ResearchStarterPlan = { loadedSkills?: { name: string; contentHash: string; packageHash: string }[]; normalizedQuestion: string; researchType: string; availableResources: string[]; missingInformation: string[]; selectedSkills: ResearchSkillSelection[]; routes: ResearchStarterRoute[]; recommendedRouteId: string; recommendationReason: string; confidence: "low" | "medium" | "high"; limitations: string[]; clarification?: ResearchClarification };
type WorkflowReviewIssues = ReviewIssues;
type WorkflowResearchDesign = { title: string; researchQuestion: string; objectives: string[]; hypotheses: string[]; populationAndSampling: string[]; variablesOrMaterials: string[]; dataCollectionPlan: string[]; analysisPlan: string[]; qualityControls: string[]; ethicsAndRisks: string[]; milestones: string[]; evidenceGaps: string[]; limitations: string[]; status: "research_design_not_empirical_result" };
type WorkflowResearchReport = { markdown: string; claimSummary: string[]; methodSummary: string; limitations: string[]; confidence: "low" | "medium" | "high" };
type AdoptResearchRouteResult = { workflow: WorkflowSaveResult; templateId: string; initialInputs: Record<string, unknown>; researchIdea: string; availableNow: boolean; starterRunId: string; researchTaskId: string; routeId: string; run?: WorkflowRunDetail };
type PendingAdoptedRoute = { starterRunId: string; researchTaskId: string; researchGoal: string; routeId: string; workflowId: string; workflowVersionId: string };
type WorkflowCandidate = { id: string; title?: string; authors?: { name?: string }[]; year?: number; venue?: string; doi?: string; sourceIds?: string[]; openAccess?: boolean; abstract?: string; sourceSha256?: string };
type WorkflowCitation = { id: string; kind?: string; reference?: string; projectId?: string; indexVersionId?: string; documentId?: string; attachmentId?: string; chunkId?: string; sourceName?: string; mimeType?: string; locator?: string; title?: string; quote?: string; quoteSha256?: string; sourceStart?: number; sourceEnd?: number };
type CandidateScreening = {
	 retrieval?: { status: "assessed" | "source_blocked" | "budget_exhausted"; supplementRounds: number; candidateCount: number; pendingCount: number };
  summary: string;
  recommendedCandidateIds: string[];
  candidateAssessments: { candidateId: string; decision: "core" | "support" | "exclude"; relevance: "high" | "medium" | "low"; reason: string; importAction?: "direct" | "verify" | "background" | "exclude"; purpose?: string }[];
  coverage: { strength: "insufficient" | "limited" | "adequate"; sufficientForClaimedScope: boolean; independentStudyEstimate: number; directPopulationMatches: number; abstractAvailable: number; metadataOnly: number; gaps: string[] };
  supplementalQueries: string[];
  recommendation: "use_recommendation" | "expand_search" | "narrow_scope";
  limitations: string[];
};
type EvidenceScreening = {
  summary: string;
  recommendedReferences: string[];
  citationAssessments: { reference: string; decision: "core" | "support" | "exclude"; sourceLevel: "full_text" | "abstract" | "metadata" | "mixed" | "unknown"; reason: string }[];
  coverage: { strength: "insufficient" | "limited" | "adequate"; sufficientForClaimedScope: boolean; independentStudyEstimate: number; directPopulationEvidence: boolean; fullTextEvidenceAvailable: boolean; gaps: string[] };
  recommendedScope: string;
  recommendation: "proceed" | "proceed_limited" | "expand_search" | "narrow_scope";
  limitations: string[];
};
type VisionFallbackChannel = { id: string; name: string; baseUrl?: string; modelId: string; apiProtocol: APIProtocol; priority: number; enabled: boolean; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number; maxTokens: number };
type Envelope = { aggregateId: string; sequence: number; type: string; payload: Record<string, unknown> };
type CreateDialog = { kind: "project"; title: string; description: string; workspacePath: string } | null;
type AppDialogKind = "confirm" | "alert" | "prompt";
type AppDialogTone = "default" | "danger";
type AppDialogOptions = { kind: AppDialogKind; title: string; message: string; confirmLabel?: string; cancelLabel?: string; tone?: AppDialogTone; initialValue?: string; placeholder?: string };
type AppDialogRequest = AppDialogOptions & { id: number; resolve: (value: boolean | string | null) => void };
type IconName = "spark" | "plus" | "chat" | "settings" | "shield" | "model" | "send" | "stop" | "play" | "pause" | "search" | "refresh" | "folder" | "check" | "close" | "back" | "trash" | "tool" | "server" | "chart" | "skill" | "paperclip" | "library" | "copy" | "archive" | "download" | "history";
type SlashCommandID = "mcp" | "skill" | "knowledge" | "compact" | "model" | "reasoning" | "permission" | "status" | "usage" | "new" | "help";
type SlashCommand = { id: SlashCommandID; name: string; title: string; description: string; icon: IconName; enabled: boolean; disabledReason?: string; state?: string; stateKind?: "on" | "off" | "loading" };
type SlashPanelMode = "mcp" | "mcp-detail" | "skill" | "knowledge" | "model" | "reasoning" | "permission" | "status" | "usage";
type SlashSkillItem = Pick<DynamicSkill, "name" | "description" | "origin" | "enabled">;

// Wails deserializes persisted records without TypeScript's compile-time
// guarantees. Older or partially written Workflow rows may contain null
// objects/arrays, so normalize every Workflow boundary before render code
// calls map/find/filter. This is deliberately a display-side repair: the
// persisted record remains untouched and the backend stays authoritative.
const workflowRecord = (value: unknown): Record<string, unknown> => value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
const workflowObjectArray = (value: unknown): Record<string, unknown>[] => Array.isArray(value) ? value.filter((item): item is Record<string, unknown> => Boolean(item && typeof item === "object" && !Array.isArray(item))) : [];
const workflowStringArray = (value: unknown): string[] => Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];

function normalizeWorkflowPort(value: unknown): WorkflowPort {
  const source = workflowRecord(value);
  return {
    ...source,
    name: typeof source.name === "string" ? source.name : "",
    type: typeof source.type === "string" ? source.type as WorkflowDataType : "any",
    description: typeof source.description === "string" ? source.description : undefined,
    fileKind: typeof source.fileKind === "string" ? source.fileKind as WorkflowPort["fileKind"] : undefined,
    control: typeof source.control === "string" ? source.control as WorkflowPort["control"] : undefined,
    minItems: typeof source.minItems === "number" ? source.minItems : undefined,
    maxItems: typeof source.maxItems === "number" ? source.maxItems : undefined,
    required: source.required === true,
  };
}

function normalizeWorkflowDefinition(value: unknown): WorkflowDefinition {
  const source = workflowRecord(value);
  const nodes = workflowObjectArray(source.nodes).map((node) => ({
    ...node,
    id: typeof node.id === "string" ? node.id : "",
    name: typeof node.name === "string" ? node.name : "未命名阶段",
    kind: typeof node.kind === "string" ? node.kind as WorkflowNodeKind : "tool",
    arguments: workflowRecord(node.arguments),
    allowedTools: workflowStringArray(node.allowedTools),
    outputSchema: workflowRecord(node.outputSchema),
  } as WorkflowNode));
  return {
    ...source,
    schemaVersion: typeof source.schemaVersion === "number" ? source.schemaVersion : 1,
    name: typeof source.name === "string" ? source.name : "科研方案",
    description: typeof source.description === "string" ? source.description : "",
    inputs: workflowObjectArray(source.inputs).map(normalizeWorkflowPort),
    nodes,
    edges: workflowObjectArray(source.edges).map((edge) => ({
      ...edge,
      fromNode: typeof edge.fromNode === "string" ? edge.fromNode : "",
      fromPort: typeof edge.fromPort === "string" ? edge.fromPort : "",
      toNode: typeof edge.toNode === "string" ? edge.toNode : "",
      toPort: typeof edge.toPort === "string" ? edge.toPort : "",
    } as WorkflowEdge)),
    outputs: workflowObjectArray(source.outputs).map((output) => ({
      ...output,
      name: typeof output.name === "string" ? output.name : "",
      type: typeof output.type === "string" ? output.type as WorkflowDataType : "any",
      fromNode: typeof output.fromNode === "string" ? output.fromNode : "",
      fromPort: typeof output.fromPort === "string" ? output.fromPort : "",
      required: output.required === true,
    } as WorkflowOutput)),
  } as WorkflowDefinition;
}

function normalizeWorkflowCompilation(value: unknown): WorkflowCompilation {
  const source = workflowRecord(value);
  const nodes = workflowObjectArray(source.nodes).map((node) => ({
    ...node,
    id: typeof node.id === "string" ? node.id : "",
    name: typeof node.name === "string" ? node.name : "未命名阶段",
    kind: typeof node.kind === "string" ? node.kind as WorkflowNodeKind : "tool",
    tool: node.tool && typeof node.tool === "object" && !Array.isArray(node.tool) ? node.tool as WorkflowToolSnapshot : undefined,
    arguments: workflowRecord(node.arguments),
    allowedTools: workflowObjectArray(node.allowedTools) as unknown as WorkflowToolSnapshot[],
    dependencies: workflowStringArray(node.dependencies),
    outputSchema: workflowRecord(node.outputSchema),
  } as WorkflowCompiledNode));
  return {
    ...source,
    order: workflowStringArray(source.order),
    nodes,
    edges: workflowObjectArray(source.edges).map((edge) => ({
      ...edge,
      fromNode: typeof edge.fromNode === "string" ? edge.fromNode : "",
      fromPort: typeof edge.fromPort === "string" ? edge.fromPort : "",
      toNode: typeof edge.toNode === "string" ? edge.toNode : "",
      toPort: typeof edge.toPort === "string" ? edge.toPort : "",
    } as WorkflowEdge)),
    inputs: workflowObjectArray(source.inputs).map(normalizeWorkflowPort),
    outputs: workflowObjectArray(source.outputs).map((output) => ({
      ...output,
      name: typeof output.name === "string" ? output.name : "",
      type: typeof output.type === "string" ? output.type as WorkflowDataType : "any",
      fromNode: typeof output.fromNode === "string" ? output.fromNode : "",
      fromPort: typeof output.fromPort === "string" ? output.fromPort : "",
      required: output.required === true,
    } as WorkflowOutput)),
    diagnostics: workflowObjectArray(source.diagnostics) as unknown as WorkflowDiagnostic[],
  } as WorkflowCompilation;
}

function normalizeWorkflowRun(value: unknown): WorkflowRun {
  const source = workflowRecord(value);
  const inputs = workflowRecord(source.inputs);
  const outputs = workflowRecord(source.outputs);
  return {
    ...source,
    id: typeof source.id === "string" ? source.id : "",
    projectId: typeof source.projectId === "string" ? source.projectId : "",
    workflowId: typeof source.workflowId === "string" ? source.workflowId : "",
    workflowVersionId: typeof source.workflowVersionId === "string" ? source.workflowVersionId : "",
    workflowName: typeof source.workflowName === "string" ? source.workflowName : "科研任务",
    workflowPurpose: source.workflowPurpose === "research_starter" ? "research_starter" : "user_plan",
    status: typeof source.status === "string" ? source.status as WorkflowRunStatus : "failed",
    inputs,
    outputs,
    compilation: normalizeWorkflowCompilation(source.compilation),
  } as WorkflowRun;
}

function normalizeWorkflow(value: unknown): ResearchWorkflow {
  const source = workflowRecord(value);
  return {
    ...source,
    id: typeof source.id === "string" ? source.id : "",
    projectId: typeof source.projectId === "string" ? source.projectId : "",
    purpose: source.purpose === "research_starter" ? "research_starter" : "user_plan",
    name: typeof source.name === "string" ? source.name : "科研方案",
    currentVersionId: typeof source.currentVersionId === "string" ? source.currentVersionId : "",
    version: typeof source.version === "number" ? source.version : 1,
  } as ResearchWorkflow;
}

function normalizeWorkflowDetail(value: unknown): WorkflowDetail {
  const source = workflowRecord(value);
  const versions = workflowObjectArray(source.versions).map((version) => ({
    ...version,
    id: typeof version.id === "string" ? version.id : "",
    workflowId: typeof version.workflowId === "string" ? version.workflowId : "",
    version: typeof version.version === "number" ? version.version : 1,
    definition: normalizeWorkflowDefinition(version.definition),
    compilation: normalizeWorkflowCompilation(version.compilation),
    runtimeInputs: workflowObjectArray(version.runtimeInputs).map(normalizeWorkflowPort),
  } as WorkflowVersion));
  return { workflow: normalizeWorkflow(source.workflow), versions };
}

function normalizeWorkflowTemplate(value: unknown): WorkflowTemplate {
  const source = workflowRecord(value);
  return {
    ...source,
    id: typeof source.id === "string" ? source.id : "",
    name: typeof source.name === "string" ? source.name : "科研方案模板",
    description: typeof source.description === "string" ? source.description : "",
    definition: normalizeWorkflowDefinition(source.definition),
  };
}

function normalizeWorkflowRunDetail(value: unknown): WorkflowRunDetail {
  const source = workflowRecord(value);
  return {
    ...source,
    run: normalizeWorkflowRun(source.run),
    steps: workflowObjectArray(source.steps) as WorkflowStep[],
    events: workflowObjectArray(source.events) as WorkflowRuntimeEvent[],
    pendingApprovals: workflowObjectArray(source.pendingApprovals) as Approval[],
    aiExecutions: workflowObjectArray(source.aiExecutions) as WorkflowAIExecution[],
    aiActivities: workflowObjectArray(source.aiActivities) as AIStageActivity[],
    toolActivities: workflowObjectArray(source.toolActivities) as WorkflowToolActivity[],
    artifactCount: typeof source.artifactCount === "number" && Number.isFinite(source.artifactCount) ? source.artifactCount : 0,
    registeredDeliverables: workflowStringArray(source.registeredDeliverables),
    revisionProposals: workflowObjectArray(source.revisionProposals) as ResearchRevisionProposal[],
  };
}

function normalizeWorkflowRunList(value: unknown): WorkflowRun[] {
  return workflowObjectArray(value).map(normalizeWorkflowRun);
}

function normalizeWorkflowTemplateList(value: unknown): WorkflowTemplate[] {
  return workflowObjectArray(value).map(normalizeWorkflowTemplate).filter((template) => template.id.length > 0);
}

function normalizeWorkflowList(value: unknown): ResearchWorkflow[] {
  return workflowObjectArray(value).map(normalizeWorkflow).filter((workflow) => workflow.id.length > 0);
}

function normalizeMessage(value: unknown): Message {
  const source = workflowRecord(value);
  const parts = workflowObjectArray(source.parts).map((part) => ({
    ...part,
    type: typeof part.type === "string" ? part.type : "text",
    text: typeof part.text === "string" ? part.text : undefined,
    payload: part.payload && typeof part.payload === "object" && !Array.isArray(part.payload) ? part.payload as AttachmentReference : undefined,
  } as MessagePart));
  return {
    ...source,
    id: typeof source.id === "string" ? source.id : `message-${Math.random().toString(36).slice(2)}`,
    runId: typeof source.runId === "string" ? source.runId : undefined,
    internal: source.internal === true,
    workflowStatus: typeof source.workflowStatus === "string" ? source.workflowStatus as WorkflowRunStatus : undefined,
    role: ["user", "assistant", "system", "tool"].includes(String(source.role)) ? source.role as Message["role"] : "assistant",
    status: typeof source.status === "string" ? source.status : "completed",
    parts,
    citations: workflowObjectArray(source.citations) as unknown as Citation[],
    reasoning: source.reasoning && typeof source.reasoning === "object" && !Array.isArray(source.reasoning) ? source.reasoning as MessageReasoning : undefined,
  };
}

function normalizeMessageList(value: unknown): Message[] {
  return workflowObjectArray(value).map(normalizeMessage);
}

function normalizeChatRun(value: unknown): Run {
  const source = workflowRecord(value);
  return {
    ...source,
    id: typeof source.id === "string" ? source.id : "",
    conversationId: typeof source.conversationId === "string" ? source.conversationId : "",
    assistantMessageId: typeof source.assistantMessageId === "string" ? source.assistantMessageId : "",
    modelProfileId: typeof source.modelProfileId === "string" ? source.modelProfileId : "",
    modelId: typeof source.modelId === "string" ? source.modelId : "",
    status: typeof source.status === "string" ? source.status : "completed",
    apiProtocol: typeof source.apiProtocol === "string" ? source.apiProtocol as APIProtocol : "openai_chat_completions",
    permissionMode: source.permissionMode === "full_access" ? "full_access" : "plan",
    requestedReasoningLevel: typeof source.requestedReasoningLevel === "string" ? source.requestedReasoningLevel as ReasoningLevel : "medium",
  } as Run;
}

function normalizeRunSnapshot(value: unknown): RunSnapshot {
  const source = workflowRecord(value);
  return {
    ...source,
    sequence: typeof source.sequence === "number" ? source.sequence : 0,
    workflowAI: source.workflowAI === true,
    run: normalizeChatRun(source.run),
    messages: normalizeMessageList(source.messages),
    toolCalls: workflowObjectArray(source.toolCalls) as unknown as ToolCall[],
    runSteps: workflowObjectArray(source.runSteps) as unknown as RunStep[],
    pendingApprovals: workflowObjectArray(source.pendingApprovals) as unknown as Approval[],
  };
}

type WorkflowStudioBoundaryProps = { children: ReactNode; resetKey: string; onRetry: () => void };
type WorkflowStudioBoundaryState = { failed: boolean };

class WorkflowStudioBoundary extends Component<WorkflowStudioBoundaryProps, WorkflowStudioBoundaryState> {
  override state: WorkflowStudioBoundaryState = { failed: false };

  static getDerivedStateFromError(): WorkflowStudioBoundaryState {
    return { failed: true };
  }

  override componentDidCatch(_error: Error, _info: ErrorInfo) {
    // Keep the failure local to the research workspace. The backend remains
    // the source of truth and the user can retry without losing other views.
  }

  override componentDidUpdate(previousProps: WorkflowStudioBoundaryProps) {
    if (previousProps.resetKey !== this.props.resetKey && this.state.failed) this.setState({ failed: false });
  }

  override render() {
    if (this.state.failed) {
      return <section className="research-workspace-error" role="alert">
        <Icon name="shield" size={25}/>
        <h2>科研任务页面暂时无法显示</h2>
        <p>当前任务记录可能不完整。任务数据仍保留在本地，重新读取即可继续。</p>
        <button type="button" onClick={this.props.onRetry}><Icon name="refresh" size={14}/>重新读取任务</button>
      </section>;
    }
    return this.props.children;
  }
}

const emptyToolCalls: ToolCall[] = [];
const emptyApprovals: Approval[] = [];
const emptyRunSteps: RunStep[] = [];
const appDialogQueue: AppDialogRequest[] = [];
const researchSideViewSession = new Map<string, ResearchSideViewChoice>();
let appDialogSequence = 0;
let appDialogListener: ((request: AppDialogRequest | null) => void) | null = null;

declare global {
  interface Window { go?: { wails?: Record<string, Record<string, (...args: unknown[]) => Promise<unknown>>> } }
}

const readFlight = singleFlight();
const viewReads = conditionalViews(8);
let viewEpoch = 0;
const viewPollMethods: Record<string, string> = {"ChatFacade.GetRunSnapshot": "PollRunSnapshot", "ChatFacade.GetLatestRunSnapshot": "PollLatestRunSnapshot", "WorkflowFacade.GetRun": "PollRun", "WorkflowFacade.ResearchTimeline": "PollResearchTimeline"};

function backend<T>(facade: string, method: string, ...args: unknown[]): Promise<T> {
  const fn = window.go?.wails?.[facade]?.[method];
  if (!fn) return Promise.reject(new Error("Wails 后端尚未连接，请通过桌面程序或 wails dev 运行。"));
  if (!/^(Get|List|ResearchTimeline)/.test(method)) {
    viewEpoch++;
    return fn(...args).finally(() => { viewEpoch++; }) as Promise<T>;
  }
  const key = `${viewEpoch}:${facade}.${method}:${JSON.stringify(args)}`;
  const pollName = viewPollMethods[`${facade}.${method}`];
  const poll = pollName ? window.go?.wails?.[facade]?.[pollName] : undefined;
  if (poll) return viewReads<T>(key, revision => poll(...args, revision) as Promise<{revision: string; unchanged: boolean; value?: T}>);
  if (/^(Get|List|ResearchTimeline)/.test(method)) return readFlight(key, () => fn(...args) as Promise<T>);
  return fn(...args) as Promise<T>;
}

function errorText(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  return "操作失败，请稍后重试。";
}

function requestAppDialog(options: AppDialogOptions): Promise<boolean | string | null> {
  return new Promise((resolve) => {
    appDialogQueue.push({ ...options, id: ++appDialogSequence, resolve });
    if (appDialogQueue.length === 1) appDialogListener?.(appDialogQueue[0] ?? null);
  });
}

async function appConfirm(options: Omit<AppDialogOptions, "kind">): Promise<boolean> {
  return await requestAppDialog({ ...options, kind: "confirm" }) === true;
}

async function appAlert(options: Omit<AppDialogOptions, "kind" | "cancelLabel">): Promise<void> {
  await requestAppDialog({ ...options, kind: "alert" });
}

async function appPrompt(options: Omit<AppDialogOptions, "kind">): Promise<string | null> {
  const value = await requestAppDialog({ ...options, kind: "prompt" });
  return typeof value === "string" ? value : null;
}

function completeAppDialog(request: AppDialogRequest, value: boolean | string | null) {
  const current = appDialogQueue[0];
  if (!current || current.id !== request.id) return;
  appDialogQueue.shift();
  current.resolve(value);
  appDialogListener?.(appDialogQueue[0] ?? null);
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

// Provider streams can contain real CR bytes or a literal escaped "\\r"
// sequence. Normalize both at the display boundary. Keep backslash commands
// such as `\\rightarrow` intact.
const normalizeDisplayText = (value: string) => value
  .replace(/\r\n?/g, "\n")
  .replace(/\\r\\n/g, "\n")
  .replace(/\\r(?![A-Za-z])/g, "\n");
const textOf = (message: Message) => normalizeDisplayText(message.parts.filter((part) => part.type === "text").map((part) => part.text ?? "").join(""));
const interruptedMessageText = (message: Message) => {
  const text = textOf(message);
  if (message.role !== "assistant" || message.internal || message.status !== "incomplete") return text;
  const segments: string[] = [];
  for (const part of message.parts) {
    const payload = part.payload as unknown as Record<string, unknown> | undefined;
    if (part.type !== "tool_result" || payload?.kind !== "run_termination_context") continue;
    if (Array.isArray(payload.activities)) {
      for (const activity of payload.activities) {
        if (activity && typeof activity.commentary === "string" && activity.commentary.trim()) {
          segments.push(normalizeDisplayText(activity.commentary).trim());
        }
      }
    }
    const draft = text.trim() || (typeof payload.draft === "string" ? normalizeDisplayText(payload.draft).trim() : "");
    // A cancelled tool turn can have the same draft in both journal and step.
    if (draft && draft !== segments[segments.length - 1]) segments.push(draft);
    return segments.join("\n\n");
  }
  return text;
};
const visibleMessageText = (message: Message) => {
  const text = interruptedMessageText(message);
  if (!message.internal || message.role !== "assistant") return text;
  return researchAnswerPresentation(text, message.citations).text;
};
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
const defaultReasoningLevel: ReasoningLevel = "high";
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
const replaceMessageText = (message: Message, text: string): Message => ({ ...message, parts: [{ type: "text", text: normalizeDisplayText(text) }, ...message.parts.filter((part) => part.type !== "text")] });
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
      && live.internal === message.internal
      && live.workflowStatus === message.workflowStatus
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

function AppDialogHost() {
  const [request, setRequest] = useState<AppDialogRequest | null>(() => appDialogQueue[0] ?? null);
  const [closing, setClosing] = useState(false);
  const [value, setValue] = useState("");
  const inputRef = useRef<HTMLInputElement | null>(null);
  const primaryRef = useRef<HTMLButtonElement | null>(null);
  const dialogRef = useRef<HTMLElement | null>(null);
  const closingTimerRef = useRef<number | null>(null);

  useEffect(() => {
    appDialogListener = (next) => { setClosing(false); setRequest(next); };
    if (appDialogQueue[0]) setRequest(appDialogQueue[0]);
    return () => {
      appDialogListener = null;
      if (closingTimerRef.current !== null) window.clearTimeout(closingTimerRef.current);
    };
  }, []);

  useEffect(() => {
    if (!request) return;
    setValue(request.initialValue ?? "");
    window.requestAnimationFrame(() => request.kind === "prompt" ? inputRef.current?.focus() : primaryRef.current?.focus());
  }, [request?.id]);

  const finish = useCallback((result: boolean | string | null) => {
    if (!request || closing) return;
    setClosing(true);
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    closingTimerRef.current = window.setTimeout(() => {
      completeAppDialog(request, result);
      closingTimerRef.current = null;
    }, reduced ? 0 : 150);
  }, [closing, request]);


  if (!request) return null;
  const confirmLabel = request.confirmLabel ?? (request.kind === "alert" ? "知道了" : request.kind === "prompt" ? "保存" : "继续");
  const cancelLabel = request.cancelLabel ?? "取消";
  const confirm = () => finish(request.kind === "prompt" ? value.trim() : true);
  return createPortal(<ModalDialog key={request.id} className={`app-dialog-backdrop ${closing ? "closing" : ""}`} busy={closing} close={() => finish(request.kind === "alert" ? true : null)}>
    <section ref={dialogRef} className={`app-dialog ${request.tone === "danger" ? "danger" : ""}`} role="dialog" aria-modal="true" aria-labelledby={`app-dialog-title-${request.id}`} aria-describedby={`app-dialog-message-${request.id}`}>
      <div className="app-dialog-symbol"><Icon name={request.tone === "danger" ? "trash" : request.kind === "prompt" ? "spark" : "shield"} size={18}/></div>
      <div className="app-dialog-copy"><h2 id={`app-dialog-title-${request.id}`}>{request.title}</h2><p id={`app-dialog-message-${request.id}`}>{request.message}</p></div>
      {request.kind === "prompt" && <input
        ref={inputRef}
        value={value}
        maxLength={160}
        placeholder={request.placeholder}
        onChange={(event) => setValue(event.target.value)}
        onKeyDown={(event) => { if (event.key === "Enter" && value.trim()) { event.preventDefault(); confirm(); } }}
      />}
      <footer>{request.kind !== "alert" && <button type="button" onClick={() => finish(null)}>{cancelLabel}</button>}<button ref={primaryRef} type="button" className="primary" disabled={request.kind === "prompt" && !value.trim()} onClick={confirm}>{confirmLabel}</button></footer>
    </section>
  </ModalDialog>, document.body);
}

export default function App() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState("");
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [conversationActivity, setConversationActivity] = useState<Record<string, string>>({});
  useEffect(() => {
    setConversationActivity({});
    if (!projectId) return;
    let disposed = false, inFlight = false, dirty = false;
    let timer: number | undefined;
    const refresh = async () => {
      if (disposed) return;
      if (inFlight) { dirty = true; return; }
      inFlight = true;
      try {
        const values = await backend<{conversationId: string; status: string}[]>("ChatFacade", "ListConversationActivity", projectId);
        if (!disposed) setConversationActivity(Object.fromEntries((values ?? []).map(value => [value.conversationId, value.status])));
      } catch { /* Keep the last known state during a transient IPC failure. */ }
      finally {
        inFlight = false;
        if (dirty && !disposed) { dirty = false; void refresh(); }
      }
    };
    const unsubscribe = eventsOn<Envelope>("sciaide:run-event", event => {
      if (!event.type.startsWith("run.") || timer !== undefined) return;
      timer = window.setTimeout(() => { timer = undefined; void refresh(); }, 100);
    });
    void refresh();
    const poll = window.setInterval(() => void refresh(), 5000);
    return () => { disposed = true; unsubscribe(); window.clearInterval(poll); window.clearTimeout(timer); };
  }, [projectId]);
  const [conversationId, setConversationId] = useState("");
  const [researchConversationId, setResearchConversationId] = useState("");
  const [researchConversation, setResearchConversation] = useState<Conversation | null>(null);
  const [researchComposerLocked, setResearchComposerLocked] = useState(false);
  const [researchRevisionConversationId, setResearchRevisionConversationId] = useState("");
  const [researchStageTasks, setResearchStageTasks] = useState<Record<string, ResearchStageTask>>({});
  const [researchTaskTimeline, setResearchTaskTimeline] = useState<ResearchTimelineView | null>(null);
  const [researchActivities, setResearchActivities] = useState<Record<string, WorkflowMessageActivity>>({});
  const [activeRunIsWorkflowAI, setActiveRunIsWorkflowAI] = useState(false);
  const [messages, setMessages] = useState<Message[]>([]);
  const [outgoingMessages, setOutgoingMessages] = useState<OutgoingMessage[]>([]);
  const sendingRef = useRef(false);
  const [sending, setSending] = useState(false);
  const acknowledgeMessages = useCallback((ids: string[]) => {
    setOutgoingMessages(current => current.filter(item => !ids.includes(item.message.id)));
  }, []);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [profileId, setProfileId] = useState("");
  const [modelId, setModelId] = useState("");
  // The topbar controls are also usable before a conversation exists.  This
  // value is the workspace default and is copied into newly-created chats or
  // Workflow Runs; an existing conversation remains the durable source.
  const [workspaceReasoningLevel, setWorkspaceReasoningLevel] = useState<ReasoningLevel>(defaultReasoningLevel);
  const [activeRun, setActiveRun] = useState<Run | null>(null);
  const [retryStatus, setRetryStatus] = useState<RetryStatus | null>(null);
  const [retryByRun, setRetryByRun] = useState<Record<string, RetryStatus>>({});
  const [toolCalls, setToolCalls] = useState<ToolCall[]>([]);
  const [runSteps, setRunSteps] = useState<RunStep[]>([]);
  const [pendingApprovals, setPendingApprovals] = useState<Approval[]>([]);
  const [resolvingApprovalId, setResolvingApprovalId] = useState("");
  const [input, setInput] = useState("");
  const [revisionLibraryOpen, setRevisionLibraryOpen] = useState(false);
  const [pendingAttachments, setPendingAttachments] = useState<Attachment[]>([]);
  const [webSearchEnabled, setWebSearchEnabled] = useState(false);
  const [importingAttachments, setImportingAttachments] = useState(false);
  const [notice, setNotice] = useState("");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [mcpOpen, setMcpOpen] = useState(false);
  const [usageOpen, setUsageOpen] = useState(false);
  const [settingsHubOpen,setSettingsHubOpen]=useState(false);
  const [searchPageOpen,setSearchPageOpen]=useState(false);
 const [networkOpen,setNetworkOpen]=useState(false);
 const [skillsOpen, setSkillsOpen] = useState(false);
  const [knowledgeOpen, setKnowledgeOpen] = useState(false);
  const [artifactsOpen, setArtifactsOpen] = useState(false);
  const [resourceTaskId, setResourceTaskId] = useState("");
  const [researchOpen, setResearchOpen] = useState(false);
  const [pythonOpen, setPythonOpen] = useState(false);
  const [workspaceMode, setWorkspaceMode] = useState<WorkspaceMode>("chat");
  const [researchWorkspaceReset, setResearchWorkspaceReset] = useState(0);
  const [archiveBusy, setArchiveBusy] = useState<"" | "export" | "restore">("");
  const [archiveReport, setArchiveReport] = useState<ProjectArchiveRestoreReport | null>(null);
  const [mcpServers, setMcpServers] = useState<MCPServer[] | null>(null);
  const [mcpStatusLoading, setMcpStatusLoading] = useState(false);
  const [mcpStatusError, setMcpStatusError] = useState(false);
  const [slashSelected, setSlashSelected] = useState(0);
  const [slashPanel, setSlashPanel] = useState<SlashPanelMode | null>(null);
  const [slashPanelLoading, setSlashPanelLoading] = useState(false);
  const [slashSkills, setSlashSkills] = useState<SlashSkillItem[]>([]);
  const [slashKnowledge, setSlashKnowledge] = useState<ResearchMaterial[]>([]);
  const [slashVision, setSlashVision] = useState<VisionFallbackChannel[]>([]);
  const [slashStatusErrors, setSlashStatusErrors] = useState<string[]>([]);
  const [slashUsage, setSlashUsage] = useState<UsageDashboardData | null>(null);
  const [slashMCPServer, setSlashMCPServer] = useState<MCPServer | null>(null);
  const [slashMCPCapabilities, setSlashMCPCapabilities] = useState<MCPCapabilities | null>(null);
  const [slashMCPAction, setSlashMCPAction] = useState(false);
  const [compacting, setCompacting] = useState(false);
  const [createDialog, setCreateDialog] = useState<CreateDialog>(null);
  const [creating,setCreating]=useState(false);
  const [busy, setBusy] = useState(false);
  const activeRunRef = useRef<Run | null>(null);
  const busyRef = useRef(false);
  const conversationOpenRequestRef = useRef(0);
  const conversationIdRef = useRef("");
  const researchConversationIdRef = useRef("");
  const researchConversationProjectIdRef = useRef("");
  const messagesRef = useRef<Message[]>([]);
  const modelSelectionRef = useRef({ profileId: "", modelId: "" });
  const restoringModelSelectionRef = useRef(false);
  const modelSelectionSaveRef = useRef<Promise<void>>(Promise.resolve());
  const chatRef = useRef<HTMLElement | null>(null);
  const composerInputRef = useRef<HTMLTextAreaElement | null>(null);
  const slashMenuRef = useRef<HTMLDivElement | null>(null);
  const pendingContentDeltasRef = useRef<Map<string, string>>(new Map());
  const contentFrameRef = useRef<number | null>(null);
  const completedContentIdsRef = useRef(new Set<string>());
  const autoFollowRef = useRef(true);
  const setTimelineFollow = useCallback((value: boolean) => { autoFollowRef.current = value; }, []);
  const messageLoadRequestRef = useRef(0);
  const snapshotRunRef = useRef<Run | null>(null);
  const researchSelectionRef = useRef(0);
  const selectedProjectIdRef = useRef(projectId);
  selectedProjectIdRef.current = projectId;
  const runSequenceRef = useRef<Map<string, number>>(new Map());
  const appliedSnapshotRef = useRef<RunSnapshot | null>(null);
  activeRunRef.current = activeRun;
  busyRef.current = busy;
  conversationIdRef.current = conversationId;
  researchConversationIdRef.current = researchConversationId;
  researchConversationProjectIdRef.current = researchConversation?.projectId ?? "";
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
    setConversations(values);
    setConversationId((current) => current && current === researchConversationIdRef.current && researchConversationProjectIdRef.current === selectedProject ? current : values.some((item) => item.id === current) ? current : first(values)?.id || "");
  }, []);
  const loadMessages = useCallback(async (selectedConversation: string) => {
    const request = ++messageLoadRequestRef.current;
    if (!selectedConversation) { setMessages([]); return; }
    const loaded = orderedMessages(normalizeMessageList(await backend<Message[]>("ConversationFacade", "ListMessages", selectedConversation)));
    if (selectedConversation !== conversationIdRef.current || request !== messageLoadRequestRef.current) return;
    setMessages((current) => mergeSnapshotMessages(current, loaded));
  }, []);
  useEffect(() => {
    if (workspaceMode === "chat" && outgoingMessages.some(item => item.conversationId === conversationId && messages.some(message => message.id === item.message.id))) {
      acknowledgeMessages(messages.map(message => message.id));
    }
  }, [messages, conversationId, workspaceMode, outgoingMessages, acknowledgeMessages]);
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
    if (!messageId || !delta || completedContentIdsRef.current.has(messageId)) return;
    const pending = pendingContentDeltasRef.current;
    pending.set(messageId, (pending.get(messageId) ?? "") + delta);
    if (contentFrameRef.current === null) contentFrameRef.current = window.requestAnimationFrame(flushContentDeltas);
  }, [flushContentDeltas]);
  const discardContentDeltas = useCallback(() => {
    pendingContentDeltasRef.current.clear();
    if (contentFrameRef.current !== null) window.cancelAnimationFrame(contentFrameRef.current);
    contentFrameRef.current = null;
  }, []);
  const resetContentAttempt = useCallback((messageId: string) => {
    messageId = messageId.trim();
    if (!messageId) return;
    completedContentIdsRef.current.delete(messageId);
    pendingContentDeltasRef.current.delete(messageId);
    setMessages(current => current.map(message => message.id === messageId ? replaceMessageText(message, "") : message));
  }, []);
  const completeStreamContent = useCallback((messageId: string, text: string) => {
    if (!messageId) return;
    completedContentIdsRef.current.add(messageId);
    pendingContentDeltasRef.current.delete(messageId);
    // Authoritative final content is applied once, never cleared or animated again.
    setMessages(current => current.map(message => message.id === messageId && textOf(message) !== normalizeDisplayText(text)
      ? replaceMessageText(message, text) : message));
  }, []);
  const applySnapshot = useCallback((snapshot: RunSnapshot) => {
    if (snapshot.run.conversationId !== conversationIdRef.current) return;
    if (appliedSnapshotRef.current === snapshot) return;
    const normalized = normalizeRunSnapshot(snapshot);
    if (normalized.run.conversationId !== conversationIdRef.current) return;
    const previousRun = snapshotRunRef.current;
    if (previousRun?.conversationId === normalized.run.conversationId && previousRun.id !== normalized.run.id && compareRunCreatedAt(previousRun.createdAt, normalized.run.createdAt) > 0) return;
    const knownSequence = runSequenceRef.current.get(normalized.run.id) ?? 0;
    if (normalized.sequence > 0 && normalized.sequence < knownSequence) return;
    appliedSnapshotRef.current = snapshot;
    if (normalized.sequence > knownSequence) runSequenceRef.current.set(normalized.run.id, normalized.sequence);
    snapshotRunRef.current = normalized.run;
    const terminal = ["completed", "failed", "cancelled", "interrupted"].includes(normalized.run.status);
    setActiveRun(current => shareSnapshot(current, normalized.run));
    setActiveRunIsWorkflowAI(Boolean(normalized.workflowAI));
    setToolCalls(current => shareSnapshot(current, normalized.toolCalls));
    setRunSteps(current => shareSnapshot(current, normalized.runSteps));
    setPendingApprovals(current => shareSnapshot(current, normalized.pendingApprovals));
    setMessages((current) => mergeSnapshotMessages(current, normalized.messages));
    if (terminal) { setRetryStatus(null); setRetryByRun((current) => applyRetryEvent(current, { aggregateId: normalized.run.id, type: "run.completed" })); }
    setBusy(!terminal);
  }, []);

  useEffect(() => { Promise.all([loadProjects(), loadProfiles()]).catch((error: unknown) => setNotice(errorText(error))); }, [loadProfiles, loadProjects]);
  useEffect(() => { loadConversations(projectId).catch((error: unknown) => setNotice(errorText(error))); }, [loadConversations, projectId]);
  useEffect(() => { setResearchConversationId(""); setResearchConversation(null); setResearchComposerLocked(false); setResearchRevisionConversationId(""); setResearchStageTasks({}); setResearchActivities({}); }, [projectId]);
  useEffect(() => {
    appliedSnapshotRef.current = null;
    discardContentDeltas(); completedContentIdsRef.current.clear(); if (workspaceMode !== "research") autoFollowRef.current = true; runSequenceRef.current.clear(); snapshotRunRef.current = null;
    setActiveRun(null); setActiveRunIsWorkflowAI(false); setRetryStatus(null); setToolCalls([]); setRunSteps([]); setPendingApprovals([]); setPendingAttachments([]); setRevisionLibraryOpen(false); setBusy(false);
    if (!conversationId) { setMessages([]); return; }
    Promise.all([
      loadMessages(conversationId),
      backend<RunSnapshot | null>("ChatFacade", "GetLatestRunSnapshot", conversationId).then((snapshot) => { if (snapshot) applySnapshot(snapshot); }),
    ]).catch((error: unknown) => setNotice(errorText(error)));
  }, [applySnapshot, conversationId, discardContentDeltas, loadMessages]);
  useLayoutEffect(() => {
    const chat = chatRef.current;
    if (chat && autoFollowRef.current) chat.scrollTo({ top: chat.scrollHeight, behavior: "instant" });
  }, [messages, outgoingMessages, activeRun?.reasoningSummary, retryStatus?.attempt, runSteps.length, toolCalls.length, pendingApprovals.length]);
  useEffect(() => {
    const chat = chatRef.current;
    if (!chat) return;
    return observeChatAutoFollow(chat, () => autoFollowRef.current);
  }, [conversationId, workspaceMode]);
  useEffect(() => onFileDrop((paths) => {
    if (projectId && conversationId && paths.length) void importDroppedDocuments(paths);
  }), [projectId, conversationId, importingAttachments]);

  useEffect(() => {
    const unsubscribe = eventsOn<Envelope>("sciaide:run-event", (event) => {
      setRetryByRun((current) => applyRetryEvent(current, event));
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
        completeStreamContent(messageId, text);
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
      if (event.type.startsWith("run.") && event.payload.run) { snapshotRunRef.current = event.payload.run as Run; setActiveRun(event.payload.run as Run); }
      if (event.type.startsWith("tool.") || event.type.startsWith("approval.")) {
        if (!activeRunIsWorkflowAI) backend<RunSnapshot>("ChatFacade", "GetRunSnapshot", run.id).then(applySnapshot).catch(() => undefined);
      }
      if (["run.completed", "run.failed", "run.cancelled", "run.interrupted"].includes(event.type)) {
        discardContentDeltas(); setRetryStatus(null); setBusy(false);
        loadMessages(conversationId).catch(() => undefined);
        loadProfiles().catch(() => undefined);
      }
    });
    return () => { unsubscribe(); discardContentDeltas(); completedContentIdsRef.current.clear(); };
  }, [activeRunIsWorkflowAI, applySnapshot, completeStreamContent, conversationId, discardContentDeltas, loadMessages, loadProfiles, queueContentDelta, resetContentAttempt]);

  useEffect(() => {
    if (!activeRun || workspaceMode === "research" || ["completed", "failed", "cancelled", "interrupted"].includes(activeRun.status)) return;
    const timer = window.setInterval(() => { if (!document.hidden) void backend<RunSnapshot>("ChatFacade", "GetRunSnapshot", activeRun.id).then(applySnapshot).catch(() => undefined); }, 1000);
    return () => window.clearInterval(timer);
  }, [activeRun?.id, activeRun?.status, applySnapshot, workspaceMode]);

  useEffect(() => {
    if (workspaceMode !== "research" || !researchConversationId || conversationId !== researchConversationId) return;
    let disposed = false, inFlight = false, nextPoll = 0;
    const refresh = async () => {
      if (disposed || inFlight || Date.now() < nextPoll) return;
      inFlight = true;
      try {
        const snapshot = await backend<RunSnapshot | null>("ChatFacade", "GetLatestRunSnapshot", researchConversationId);
        if (!disposed && snapshot) {
          applySnapshot(snapshot);
          nextPoll = Date.now() + (["completed", "failed", "cancelled", "interrupted"].includes(snapshot.run.status) ? 5000 : 1000);
        }
      }
      catch { /* Keep the last snapshot; the next poll can recover. */ }
      finally { inFlight = false; }
    };
    void refresh();
    const timer = window.setInterval(() => { if (!document.hidden) void refresh(); }, 1500);
    return () => { disposed = true; window.clearInterval(timer); };
  }, [applySnapshot, conversationId, researchConversationId, workspaceMode]);

  async function submitCreate(event: FormEvent) {
    event.preventDefault(); if (creating || !createDialog?.title.trim()) return;
    setCreating(true);
    try {
      const created = await backend<Project>("ProjectFacade", "CreateProject", { name: createDialog.title.trim(), description: createDialog.description.trim(), workspacePath: createDialog.workspacePath.trim() });
      await loadProjects(); setProjectId(created.id);
      setCreateDialog(null);
    } catch (error) { setNotice(errorText(error)); } finally {setCreating(false);}
  }

  const creatingConversationRef = useRef(false);
  async function createConversation() {
    if (!projectId || creatingConversationRef.current) return;
    const targetProject = projectId;
    creatingConversationRef.current = true; setCreating(true); setNotice("");
    try {
      const created = await backend<Conversation>("ConversationFacade", "CreateConversation", {
        projectId: targetProject, title: "", reasoningLevel: workspaceReasoningLevel,
        ...(profileId && modelId ? { modelProfileId: profileId, modelId } : {}),
      });
      if (selectedProjectIdRef.current !== targetProject) return;
      setConversations(current => [created, ...current.filter(item => item.id !== created.id)]);
      setConversationId(created.id);
      window.requestAnimationFrame(() => composerInputRef.current?.focus());
    } catch (error) {
      if (selectedProjectIdRef.current === targetProject) setNotice(errorText(error));
    } finally { creatingConversationRef.current = false; setCreating(false); }
  }

  const [cancellingRunId, setCancellingRunId] = useState("");
  const cancellationRef = useRef("");
  async function stopConversationRun() {
    const run = activeRun;
    if (!run || !busy || sendingRef.current || activeRunIsWorkflowAI || researchConversationLocked || run.conversationId !== conversationId || cancellationRef.current === run.id) return;
    cancellationRef.current = run.id; setCancellingRunId(run.id);
    try {
      await backend<void>("ChatFacade", "CancelRun", run.id);
      // The backend owns terminal state. Do not enable sending before cancellation completes.
    } catch (error) {
      if (conversationIdRef.current === run.conversationId) setNotice(errorText(error));
      if (cancellationRef.current === run.id) { cancellationRef.current = ""; setCancellingRunId(""); }
    }
  }
  useEffect(() => {
    if (!busy || activeRun?.id !== cancellationRef.current) { cancellationRef.current = ""; setCancellingRunId(""); }
  }, [busy, activeRun?.id, conversationId]);

  async function send(event: FormEvent) {
    event.preventDefault(); const text = input.trim();
    if (sendingRef.current || busy || researchConversationLocked) return;
    const localCommand = slashCommands.find((item) => `/${item.name}` === text.toLowerCase());
    if (localCommand) {
      await executeSlashCommand(localCommand);
      return;
    }
    if ((!text && pendingAttachments.length === 0) || !conversationId || !profileId || !modelId) return;
    if (workspaceMode === "research") window.dispatchEvent(new CustomEvent("research-discussion-sent", { detail: conversationId }));
    const submittedAttachments = pendingAttachments;
    const submittedConversation = conversationId, submittedProject = projectId;
    const taskId = workspaceMode === "research" && researchTaskTimeline?.projectId === projectId ? researchTaskTimeline.taskId : "";
    const messageId = crypto.randomUUID();
    const optimistic: Message = {id: messageId, internal: false, role: "user", status: "sending", createdAt: new Date().toISOString(), parts: [
      ...(text ? [{type: "text", text}] : []),
      ...submittedAttachments.map(item => ({type: "media", payload: {attachmentId: item.id, originalName: item.originalName, mimeType: item.mimeType, format: item.format, sizeBytes: item.sizeBytes, unitCount: item.unitCount ?? 0, truncated: false}})),
    ]};
    sendingRef.current = true; setSending(true);
    setOutgoingMessages(current => [...current.filter(item => item.message.status !== "send_failed" || item.conversationId !== submittedConversation), {projectId: submittedProject, taskId, conversationId: submittedConversation, message: optimistic}]);
    autoFollowRef.current = true;
    setNotice(""); setInput(""); setPendingAttachments([]); setRetryStatus(null); setBusy(true);
    try {
      const command = { conversationId, webSearchEnabled, modelProfileId: profileId, modelId, reasoningLevel: selectedConversation?.reasoningLevel ?? workspaceReasoningLevel, text, attachmentIds: submittedAttachments.map((item) => item.id), clientMessageId: messageId };
      const run = await backend<Run>("ChatFacade", "StartChat", command);
      setOutgoingMessages(current => current.map(item => item.message.id === messageId ? {...item, message: {...item.message, runId: run.id, createdAt: run.createdAt, status: "complete"}} : item));
      if (taskId) window.dispatchEvent(new CustomEvent("research-message-saved", {detail: {projectId: submittedProject, taskId}}));
      if (submittedConversation === conversationIdRef.current && submittedProject === selectedProjectIdRef.current) setActiveRun(run);
      // These are post-save refreshes. A failed refresh must never resend an accepted message.
      void backend<Conversation>("ConversationFacade", "GetConversation", submittedConversation).then(persisted => {
        if (submittedProject !== selectedProjectIdRef.current || submittedConversation !== conversationIdRef.current) return;
        if (persisted.id === researchConversationIdRef.current) setResearchConversation(persisted);
        else setConversations(current => current.map(item => item.id === persisted.id ? persisted : item));
      }).catch(() => undefined);
      if (submittedConversation === conversationIdRef.current && submittedProject === selectedProjectIdRef.current) void loadMessages(submittedConversation).catch(() => undefined);
    } catch (error) {
      setOutgoingMessages(current => current.map(item => item.message.id === messageId ? {...item, message: {...item.message, status: "send_failed"}} : item));
      if (submittedConversation === conversationIdRef.current && submittedProject === selectedProjectIdRef.current) {
        setInput(current => current || text); setPendingAttachments(current => current.length ? current : submittedAttachments); setBusy(false); setNotice(errorText(error));
      }
    } finally { sendingRef.current = false; setSending(false); }
  }

  async function attachDocuments() {
    if (!projectId || importingAttachments) return;
    setImportingAttachments(true); setNotice("");
    try {
      const result = await backend<AttachmentImportBatch>("AttachmentFacade", "ChooseAndImportConversationDocuments", projectId, conversationId);
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
      const result = await backend<AttachmentImportBatch>("AttachmentFacade", "ImportConversationDocumentPaths", projectId, conversationId, paths);
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
    try { setSlashKnowledge(await backend<ResearchMaterial[]>("KnowledgeFacade", "ListLibraryMaterials", projectId)); }
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
      (projectId ? backend<ResearchMaterial[]>("KnowledgeFacade", "ListLibraryMaterials", projectId) : Promise.resolve([])).catch(() => { errors.push("资料库"); return null; }),
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
  const selectedConversation = conversationId === researchConversationId ? researchConversation ?? undefined : conversations.find((item) => item.id === conversationId);
  const selectResearchConversation = useCallback(async (nextConversationId: string) => {
    const request = ++researchSelectionRef.current;
    nextConversationId = nextConversationId.trim();
    if (!projectId) return;
    const values = await backend<Conversation[]>("ConversationFacade", "ListConversations", projectId);
    if (request !== researchSelectionRef.current || selectedProjectIdRef.current !== projectId) return;
    setConversations(values);
    if (!nextConversationId) {
      setResearchConversationId("");
      setResearchConversation(null);
      setResearchStageTasks({});
      setResearchActivities({});
      if (conversationIdRef.current && !values.some((item) => item.id === conversationIdRef.current)) setConversationId(values[0]?.id ?? "");
      return;
    }
    const loaded = await backend<Conversation>("ConversationFacade", "GetConversation", nextConversationId);
    if (request !== researchSelectionRef.current || selectedProjectIdRef.current !== projectId) return;
    if (loaded.projectId !== projectId) {
      setResearchConversationId("");
      setResearchConversation(null);
      setResearchStageTasks({});
      setResearchActivities({});
      throw new Error("科研任务绑定的协作会话不存在。");
    }
    setResearchConversation(loaded);
    setResearchConversationId(nextConversationId);
    setConversationId(nextConversationId);
  }, [projectId]);
  const enterChatMode = useCallback(() => {
    setWorkspaceMode("chat");
    if (conversationIdRef.current === researchConversationIdRef.current) setConversationId(first(conversations)?.id ?? "");
  }, [conversations]);

  // History rows can represent either ordinary chat or a Workflow-bound
  // research conversation. Resolve the durable binding before choosing the UI
  // mode so reopening an old research Run restores its per-turn activity
  // records instead of silently rendering an empty/free-chat view.
  const openConversation = useCallback(async (conversation: Conversation) => {
    const request = ++conversationOpenRequestRef.current;
    try {
      const lookup = await backend<WorkflowConversationLookup>("WorkflowFacade", "GetRunByConversation", conversation.id);
      if (request !== conversationOpenRequestRef.current) return;
      const boundRun = lookup?.found ? lookup.run : undefined;
      if (boundRun?.run?.projectId === projectId) {
        setWorkspaceMode("research");
        setResearchConversation(conversation);
        setResearchConversationId(conversation.id);
        setConversationId(conversation.id);
        return;
      }
    } catch {
      // A lookup failure must not make an ordinary conversation unusable. The
      // research workspace will still surface a concrete error when opened via
      // its task list, while this history action falls back to chat.
    }
    if (request !== conversationOpenRequestRef.current) return;
    setResearchConversationId("");
    setResearchConversation(null);
    setResearchStageTasks({});
    setResearchActivities({});
    setWorkspaceMode("chat");
    setConversationId(conversation.id);
  }, [projectId]);
  useEffect(() => {
    if (workspaceMode === "research" && !selectedProject) setWorkspaceMode("chat");
  }, [workspaceMode, selectedProject]);
  const selectedProfile = profiles.find((item) => item.id === profileId);
  const selectedModel = selectedProfile?.models.find((item) => item.id === modelId);
  const selectableModels = useMemo(() => profiles.filter((profile) => profile.enabled).flatMap((profile) => profile.models.filter((model) => model.enabled).map((model) => ({ profile, model }))), [profiles]);
  const selectedModelKey = profileId && modelId ? modelKey(profileId, modelId) : "";
  const settingsConversation = workspaceMode === "research" && !researchConversationId ? undefined : selectedConversation;
  const effectiveReasoningLevel = settingsConversation?.reasoningLevel ?? workspaceReasoningLevel;
  const reasoning = reasoningDisplay(effectiveReasoningLevel, selectedModel, activeRun, profileId, modelId);
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
    { id: "knowledge", name: "knowledge", title: "研究资料库", description: "查看项目资料及其可用状态", icon: "library", enabled: Boolean(selectedProject), disabledReason: "请先选择科研项目" },
    { id: "compact", name: "compact", title: "压缩会话", description: "生成可校验 checkpoint 并释放上下文", icon: "refresh", enabled: Boolean(selectedConversation && messages.length && !busy && !compacting), disabledReason: busy ? "请等待当前回答完成" : compacting ? "会话正在压缩" : "当前会话还没有可压缩内容", state: compacting ? "压缩中" : activeRun?.contextCompacted ? "已有 checkpoint" : undefined, stateKind: compacting ? "loading" : activeRun?.contextCompacted ? "on" : undefined },
    { id: "model", name: "model", title: "模型", description: "选择当前会话使用的模型", icon: "model", enabled: selectableModels.length > 0, disabledReason: "还没有可用模型" },
    { id: "reasoning", name: "reasoning", title: "思考强度", description: settingsConversation ? "切换当前会话的推理档位" : "设置下一次对话或科研任务的推理档位", icon: "spark", enabled: Boolean(!busy && selectableModels.length > 0), disabledReason: busy ? "运行期间不能切换思考强度" : "请先配置可用模型", state: effectiveReasoningLevel, stateKind: "on" },
    { id: "permission", name: "permission", title: "工具权限", description: "切换 Plan 或 Full Access", icon: "shield", enabled: Boolean(selectedConversation && !busy), disabledReason: busy ? "运行期间不能切换工具权限" : "请先选择研究会话", state: selectedConversation?.permissionMode === "full_access" ? "Full Access" : selectedConversation ? "Plan" : undefined, stateKind: "on" },
    { id: "status", name: "status", title: "运行状态", description: "汇总模型、上下文和扩展能力状态", icon: "chart", enabled: Boolean(selectedConversation), disabledReason: "请先选择研究会话", state: busy ? "运行中" : "就绪", stateKind: busy ? "loading" : "on" },
    { id: "usage", name: "usage", title: "用量统计", description: "查看 Token、推理与缓存命中摘要", icon: "chart", enabled: true },
    { id: "new", name: "new", title: "新建研究会话", description: "在当前项目开始新的会话", icon: "plus", enabled: Boolean(selectedProject && !busy), disabledReason: busy ? "请先停止当前回答" : "请先选择科研项目" },
    { id: "help", name: "help", title: "命令列表", description: "查看当前可用的斜杠命令", icon: "search", enabled: true },
  ], [activeRun?.contextCompacted, busy, compacting, effectiveReasoningLevel, mcpStatus.kind, mcpStatus.text, messages.length, selectableModels.length, selectedConversation, selectedProject, settingsConversation]);
  const filteredSlashCommands = useMemo(() => slashCommands.filter((item) => !slashQuery || item.name.includes(slashQuery) || item.title.toLowerCase().includes(slashQuery) || item.description.toLowerCase().includes(slashQuery)), [slashCommands, slashQuery]);
  const exactSlashCommand = slashCommands.find((item) => `/${item.name}` === input.trim().toLowerCase());
  const slashPanelItemCount = slashPanelLoading ? 0 : slashPanel === "mcp" ? (mcpServers?.length ?? 0) + 1 : slashPanel === "skill" ? slashSkills.length + 1 : slashPanel === "model" ? selectableModels.length + 1 : slashPanel === "reasoning" ? reasoningLevels.length : slashPanel === "permission" ? 2 : slashPanel === "mcp-detail" ? 2 : slashPanel === "knowledge" || slashPanel === "usage" ? 1 : 0;
  const slashPanelTitle = slashPanel === "mcp" ? "MCP Servers" : slashPanel === "mcp-detail" ? slashMCPServer?.name ?? "MCP Server" : slashPanel === "skill" ? "Skills" : slashPanel === "knowledge" ? "研究资料库" : slashPanel === "model" ? "选择模型" : slashPanel === "reasoning" ? "思考强度" : slashPanel === "permission" ? "工具权限" : slashPanel === "status" ? "运行状态" : "用量统计";
  const slashPanelMeta = slashPanelLoading ? "正在读取" : slashPanel === "mcp" ? `${mcpServers?.length ?? 0} 个 Server` : slashPanel === "skill" ? `${slashSkills.length} 个 Skill` : slashPanel === "knowledge" ? `${slashKnowledge.length} 篇文档` : slashPanel === "model" ? `${selectableModels.length} 个模型` : slashPanel === "reasoning" ? `${reasoningLevels.length} 个档位` : slashPanel === "permission" ? "2 种模式" : slashPanel === "status" ? selectedConversation?.title ?? "当前会话" : "全部模型";

  useEffect(() => {
    if (slashPanel === "reasoning") { setSlashSelected(Math.max(0, reasoningLevels.indexOf(effectiveReasoningLevel))); return; }
    if (slashPanel === "permission") { setSlashSelected(selectedConversation?.permissionMode === "full_access" ? 1 : 0); return; }
    setSlashSelected(0);
  }, [effectiveReasoningLevel, selectedConversation?.permissionMode, selectedConversation?.reasoningLevel, slashPanel, slashQuery]);
  useEffect(() => { if (slashPanel === null && slashTyping) void loadMCPStatus(); }, [loadMCPStatus, slashPanel, slashTyping]);
  useEffect(() => {
    const autoDismiss = notice.startsWith("压缩成功：") || notice.startsWith("压缩部分完成：") || notice.startsWith("已设置下一次对话/科研任务的思考强度：") || notice.startsWith("思考强度：");
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
      void createConversation();
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
      .then((updated) => { if (updated.id === researchConversationIdRef.current) setResearchConversation(updated); else setConversations((current) => current.map((item) => item.id === updated.id ? updated : item)); })
      .catch((error: unknown) => { setNotice(errorText(error)); });
  }, [modelId, profileId, selectedConversation]);

  const resolveApproval = useCallback(async (approval: Approval, allow: boolean) => {
    if (resolvingApprovalId) return;
    setResolvingApprovalId(approval.id);
    try {
      await backend("PermissionFacade", "ResolveApproval", { approvalId: approval.id, allow, scope: "call" });
      setPendingApprovals(current => current.filter(item => item.id !== approval.id));
      applySnapshot(normalizeRunSnapshot(await backend<RunSnapshot>("ChatFacade", "GetRunSnapshot", approval.runId)));
    } catch (error) { setNotice(errorText(error)); }
    finally { setResolvingApprovalId(""); }
  }, [applySnapshot, resolvingApprovalId]);

  const researchConversationLocked = workspaceMode === "research" && conversationId === researchConversationId && researchComposerLocked;
  const researchRevisionReady = workspaceMode === "research" && Boolean(conversationId) && conversationId === researchConversationId && conversationId === researchRevisionConversationId;
  const openArtifactsForTask = useCallback((taskId = "") => { setResourceTaskId(taskId.trim()); setArtifactsOpen(true); }, []);
  const openKnowledgeForTask = useCallback((taskId = "") => { setResourceTaskId(taskId.trim()); setKnowledgeOpen(true); }, []);

  const renderMessage = (message: Message) => {
    const messageRun = message.role === "assistant" && activeRun?.id === message.runId ? activeRun : undefined;
    return <MessageRow key={message.id} message={message} providerName={selectedProfile?.name ?? "SciAide"} run={messageRun ?? undefined}
      runActive={Boolean(messageRun && busy)} retryStatus={message.runId ? retryByRun[message.runId] ?? null : null}
      runSteps={messageRun ? runSteps : emptyRunSteps} toolCalls={messageRun ? toolCalls : emptyToolCalls} approvals={messageRun ? pendingApprovals : emptyApprovals}
      revealing={Boolean(messageRun && busy && message.status === "streaming" && visibleMessageText(message))} resolvingApprovalId={messageRun ? resolvingApprovalId : ""} resolveApproval={resolveApproval} saveArtifact={saveMessageArtifact}
      researchStageTask={message.runId ? researchStageTasks[message.runId] : undefined} workflowActivity={message.runId ? researchActivities[message.runId] : undefined}
      resolveResearchApproval={message.runId ? researchActivities[message.runId]?.resolveApproval : undefined}/>;
  };
  return <div className={`app-shell mode-${workspaceMode}`}>
	<div className="window-titlebar" onDoubleClick={(event) => { if (!(event.target instanceof Element) || !event.target.closest(".window-controls")) toggleMaximiseWindow(); }}><div className="window-brand"><span><Icon name="spark" size={13}/></span><b>SciAide</b>{workspaceMode === "research" && <em>科研模式</em>}</div><div className="window-controls"><button type="button" aria-label="最小化窗口" title="最小化" onClick={minimiseWindow}>—</button><button type="button" aria-label="最大化或还原窗口" title="最大化/还原" onClick={toggleMaximiseWindow}>□</button><button type="button" className="window-close" aria-label="关闭窗口" title="关闭" onClick={quitApplication}>×</button></div></div>
    <aside className="sidebar">
      <div className="logo"><span><Icon name="spark" size={21}/></span><div><strong>SciAide</strong><small>Research Copilot</small></div></div>
      <div className="project-create-actions"><button className="new-project" onClick={() => setCreateDialog({ kind: "project", title: "", description: "", workspacePath: "" })} disabled={Boolean(archiveBusy)}><Icon name="plus"/> 新建科研项目</button><button type="button" className="project-restore" title="从备份导入项目" aria-label="从备份导入项目" disabled={Boolean(archiveBusy)} onClick={() => void restoreProjectArchive()}><Icon name={archiveBusy === "restore" ? "refresh" : "download"} size={16}/></button></div>
      <div className="project-block"><label className="field-label" htmlFor="project">WORKSPACE</label><div className={`project-actions ${selectedProject ? "has-project" : ""}`}><div className="select-shell"><Icon name="folder" size={16}/><select id="project" value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="">选择项目</option>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></div>{selectedProject && <><button className="icon-project-action" title="导出项目备份" aria-label="导出项目备份" disabled={Boolean(archiveBusy)} onClick={() => void exportProjectArchive(selectedProject)}><Icon name={archiveBusy === "export" ? "refresh" : "archive"} size={15}/></button><button className="icon-danger" title="从 SciAide 移除项目" aria-label="从 SciAide 移除项目" disabled={Boolean(archiveBusy)} onClick={() => void removeProject(selectedProject)}><Icon name="trash" size={15}/></button></>}</div>{selectedProject && <small className="workspace-path" title={selectedProject.workspacePath}>{selectedProject.workspaceKind === "external" ? "外部目录" : "SciAide 托管"} · {selectedProject.workspacePath}</small>}</div>
      <div className="section-title"><span>研究会话</span><button aria-label="新建会话" onClick={() => void createConversation()} disabled={!projectId || creating}><Icon name="plus" size={17}/></button></div>
      <nav className="conversation-list">{conversations.length ? conversations.map((conversation) => <div className={`conversation-row ${conversation.id === conversationId ? "active" : ""}`} key={conversation.id}><button onClick={() => void openConversation(conversation)}><Icon name="chat" size={16}/><span>{conversation.title}</span></button>{conversationActivity[conversation.id] && <span className={`conversation-running ${conversationActivity[conversation.id]}`} role="status" aria-label={conversationActivity[conversation.id] === "waiting_approval" ? "等待确认" : "会话正在运行"} title={conversationActivity[conversation.id] === "waiting_approval" ? "等待确认" : conversationActivity[conversation.id] === "queued" ? "准备运行" : "正在运行"}/>}<button className="conversation-remove" title="移除会话" onClick={() => void removeConversation(conversation)}><Icon name="close" size={13}/></button></div>) : <p className="sidebar-empty">{projectId ? "还没有自由会话，点击右上角 ＋ 创建" : "选择项目后显示会话"}</p>}</nav>
      <div className="sidebar-footer"><button onClick={() => setUsageOpen(true)}><span className="nav-icon"><Icon name="chart" size={17}/></span><span><b>用量统计</b><small>全部模型 · 日期与缓存命中</small></span></button><button onClick={() => {setSettingsHubOpen(true);setSettingsOpen(true);}}><span className="nav-icon"><Icon name="settings" size={17}/></span><span><b>设置</b><small>模型、搜索、Skills 与网络</small></span></button></div>
    </aside>

	<main className="workspace">
	  <header className="topbar"><div className="mode-context"><nav className="workspace-mode-switch" aria-label="工作模式"><button type="button" className={workspaceMode === "chat" ? "selected" : ""} onClick={enterChatMode}><Icon name="chat" size={15}/>自由对话</button><button type="button" className={workspaceMode === "research" ? "selected" : ""} disabled={!selectedProject} title={selectedProject ? "进入项目科研模式" : "请先选择项目"} onClick={() => setWorkspaceMode("research")}><Icon name="history" size={15}/>科研模式</button></nav><div className="breadcrumbs"><span>{selectedProject?.name ?? "Workspace"}</span><i>/</i><strong>{workspaceMode === "research" ? researchConversationId ? selectedConversation?.title ?? "科研协作会话" : "研究任务工作台" : selectedConversation?.title ?? "新研究"}</strong></div></div><div className="top-actions"><button type="button" className="research-open" aria-label="打开文献发现" title={selectedProject ? `检索并筛选 ${selectedProject.name} 的研究文献` : "请先选择项目"} disabled={!selectedProject} onClick={() => setResearchOpen(true)}><Icon name="search" size={15}/><span>文献发现</span></button><button type="button" className="python-open" aria-label="打开项目 Python 环境" title={selectedProject ? `管理 ${selectedProject.name} 的 Python 环境` : "请先选择项目"} disabled={!selectedProject} onClick={() => setPythonOpen(true)}><Icon name="tool" size={15}/><span>Python 环境</span></button><button type="button" className="artifact-open" aria-label="打开科研产物" title={selectedProject ? `查看 ${selectedProject.name} 的科研产物` : "请先选择项目"} disabled={!selectedProject} onClick={() => setArtifactsOpen(true)}><Icon name="archive" size={15}/><span>科研产物</span></button><button type="button" className="knowledge-open" aria-label="打开项目研究资料库" title={selectedProject ? `管理 ${selectedProject.name} 的研究资料` : "请先选择项目"} disabled={!selectedProject} onClick={() => setKnowledgeOpen(true)}><Icon name="library" size={15}/><span>研究资料库</span></button>{workspaceMode === "chat" && <div className="permission-picker" title={busy ? "运行期间不能切换权限模式" : "当前 Workspace 内只读免确认；外部读取、写入和其他工具需确认"}><Icon name="shield" size={13}/><select aria-label="工具权限模式" value={selectedConversation?.permissionMode ?? "plan"} disabled={!selectedConversation || busy} onChange={(event) => void changePermissionMode(event.target.value as PermissionMode)}><option value="plan">Plan · 写入/工具确认</option><option value="full_access">Full Access</option></select></div>}<div className="model-picker"><span className={selectedProfile?.secretConfigured ? "status-dot ready" : "status-dot"}/><select aria-label="选择模型" value={selectedModelKey} onChange={(event) => { const [nextProfile, nextModel] = splitModelKey(event.target.value); setProfileId(nextProfile); setModelId(nextModel); }}><option value="">选择模型</option>{selectableModels.map(({ profile, model }) => <option key={modelKey(profile.id, model.id)} value={modelKey(profile.id, model.id)}>{profile.name} · {model.id}</option>)}</select></div></div></header>
      <div className="workspace-content">
      {workspaceMode === "research" && selectedProject && <WorkflowStudioBoundary resetKey={`${selectedProject.id}:${researchConversationId}:${researchWorkspaceReset}`} onRetry={() => setResearchWorkspaceReset((value) => value + 1)}>
        <Fragment key={researchWorkspaceReset}>
        <WorkflowStudio
          key={selectedProject.id}
          project={selectedProject}
          initialConversationId={researchConversationId}
          modelProfileId={profileId}
          modelId={modelId}
          reasoningLevel={effectiveReasoningLevel}
          pythonDialogOpen={pythonOpen}
          openPython={() => setPythonOpen(true)}
          openArtifacts={openArtifactsForTask}
          selectConversation={selectResearchConversation}
          publishStageTasks={setResearchStageTasks}
          publishResearchActivities={setResearchActivities}
          publishTaskTimeline={setResearchTaskTimeline}
          publishComposerLocked={setResearchComposerLocked}
          publishRevisionConversationId={setResearchRevisionConversationId}
        />
        </Fragment>
      </WorkflowStudioBoundary>}
      <section className="chat" aria-live="polite" ref={chatRef} onScroll={(event) => {
        const chat = event.currentTarget;
        autoFollowRef.current = chat.scrollHeight - chat.scrollTop - chat.clientHeight < 96;
      }}>
        {workspaceMode === "research" && researchTaskTimeline?.projectId === projectId ? <ResearchTaskTimeline key={`${projectId}:${researchTaskTimeline.taskId}`} projectId={projectId} taskId={researchTaskTimeline.taskId} read={readResearchTimeline} follow={setTimelineFollow}
          pending={outgoingMessages.filter(item => item.projectId === projectId && item.taskId === researchTaskTimeline.taskId).map(item => ({id: `message:${item.message.id}`, sequence: "0", runId: "", conversationId: item.conversationId, kind: "message", createdAt: item.message.createdAt ?? "", snapshot: {}, message: item.message, active: false}))}
          acknowledge={acknowledgeMessages} render={entry => {
          if (entry.message) {
            const live = entry.conversationId === conversationId ? messages.find(message => message.id === entry.message!.id) : undefined;
            return renderMessage(live && entry.message.status !== "complete" ? live : entry.message);
          }
          return researchTaskTimeline.render(entry);
        }}/> : messages.length === 0 && !outgoingMessages.some(item => item.projectId === projectId && item.conversationId === conversationId)
          ? workspaceMode === "research" && conversationId === researchConversationId
            ? <ResearchChatEmpty hasProfile={Boolean(profileId && modelId)} openSettings={() => setSettingsOpen(true)} setPrompt={setInput}/>
            : <EmptyState hasProject={Boolean(projectId)} hasConversation={Boolean(conversationId)} hasProfile={Boolean(profileId && modelId)} openSettings={() => setSettingsOpen(true)} createConversation={() => void createConversation()} setPrompt={setInput}/>
          : <div className="message-stack">{[...messages, ...outgoingMessages.filter(item => item.projectId === projectId && item.conversationId === conversationId && !messages.some(message => message.id === item.message.id)).map(item => item.message)].map(renderMessage)}</div>}
      </section>
      </div>
      <footer className="composer-wrap">
        {notice && <div className="notice"><Icon name="shield" size={15}/><span>{notice}</span><button onClick={() => setNotice("")}><Icon name="close" size={14}/></button></div>}
        {activeRun?.errorMessage && <RunErrorNotice run={activeRun}/>}
        {slashMenuOpen && <div ref={slashMenuRef} className="slash-command-menu" role="listbox" aria-label="斜杠命令">
          {slashPanel === null && <><header><b>命令</b><span>{filteredSlashCommands.length} 项</span></header><div>{filteredSlashCommands.length ? filteredSlashCommands.map((command, index) => <button type="button" role="option" aria-selected={index === slashSelected} className={index === slashSelected ? "selected" : ""} key={command.id} disabled={!command.enabled} onMouseDown={(event) => { event.preventDefault(); void executeSlashCommand(command); }} onMouseEnter={() => setSlashSelected(index)}><span className="slash-command-icon"><Icon name={command.icon} size={16}/></span><span className="slash-command-copy"><b>/{command.name} <i>{command.title}</i></b><small>{command.enabled ? command.description : command.disabledReason}</small></span>{command.state && <span className={`slash-command-state ${command.stateKind ?? "off"}`}><i/>{command.state}</span>}</button>) : <p>没有匹配的本地命令</p>}</div></>}
          {slashPanel !== null && <header className="slash-panel-header"><button type="button" aria-label="返回命令列表" onMouseDown={(event) => { event.preventDefault(); if (slashPanel === "mcp-detail") { setSlashPanel("mcp"); setSlashMCPServer(null); setSlashMCPCapabilities(null); } else { setSlashPanel(null); setInput("/"); } }}><Icon name="back" size={15}/></button><b>{slashPanelTitle}</b><span>{slashPanelMeta}</span></header>}
          {slashPanel === "mcp" && <div className="slash-runtime-list">{slashPanelLoading ? <p>正在读取 MCP 运行状态…</p> : mcpStatusError ? <p>无法读取 MCP 状态</p> : mcpServers?.length ? mcpServers.map((server, index) => { const state = mcpRuntimeState(server); return <button type="button" className={slashSelected === index ? "selected" : ""} key={server.id} onMouseEnter={() => setSlashSelected(index)} onMouseDown={(event) => { event.preventDefault(); void openSlashMCPServer(server); }}><span className="slash-command-icon"><Icon name="server" size={16}/></span><span className="slash-command-copy"><b>{server.name} <i>{server.namespace}</i></b><small>{server.toolCount} tools · {server.resourceCount} resources · {server.promptCount} prompts{server.lastError ? ` · ${server.lastError}` : ""}</small></span><span className={`slash-command-state ${state.kind}`}><i/>{state.label}</span></button>; }) : <p>还没有配置 MCP Server</p>}<button type="button" className={`slash-manage ${slashSelected === (mcpServers?.length ?? 0) ? "selected" : ""}`} onMouseEnter={() => setSlashSelected(mcpServers?.length ?? 0)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setMcpOpen(true); }}><Icon name="settings" size={14}/> 管理 MCP 配置</button></div>}
          {slashPanel === "mcp-detail" && slashMCPServer && <div className="slash-runtime-detail"><div className="slash-runtime-summary"><span className={`slash-command-state ${mcpRuntimeState(slashMCPServer).kind}`}><i/>{mcpRuntimeState(slashMCPServer).label}</span><b>{slashMCPServer.toolCount} tools · {slashMCPServer.resourceCount} resources · {slashMCPServer.promptCount} prompts</b></div>{slashPanelLoading ? <p>正在读取 Server 能力…</p> : slashMCPCapabilities ? <><section><b>Tools</b>{slashMCPCapabilities.tools.length ? <div className="slash-tool-list">{slashMCPCapabilities.tools.map((tool) => <span key={tool.qualifiedName} title={tool.description}><code>{tool.originalName}</code><small>{tool.description || tool.qualifiedName}</small></span>)}</div> : <p>此 Server 没有暴露工具</p>}</section>{slashMCPCapabilities.resources.length > 0 && <section><b>Resources</b><p>{slashMCPCapabilities.resources.join(" · ")}</p></section>}{slashMCPCapabilities.prompts.length > 0 && <section><b>Prompts</b><p>{slashMCPCapabilities.prompts.join(" · ")}</p></section>}</> : <p>{slashMCPServer.lastError || "Server 关闭时不加载能力列表。"}</p>}<div className="slash-detail-actions"><button type="button" className={slashSelected === 0 ? "selected" : ""} disabled={slashMCPAction || slashMCPServer.status === "starting" || slashMCPServer.status === "initializing" || slashMCPServer.status === "stopping" || (!(slashMCPServer.status === "ready" || slashMCPServer.status === "degraded") && (!slashMCPServer.enabled || slashMCPServer.trust !== "user_trusted"))} onMouseEnter={() => setSlashSelected(0)} onMouseDown={(event) => { event.preventDefault(); void toggleSlashMCPServer(slashMCPServer); }}><Icon name={slashMCPAction ? "refresh" : slashMCPServer.status === "ready" || slashMCPServer.status === "degraded" ? "stop" : "server"} size={14}/>{slashMCPAction ? "处理中" : slashMCPServer.status === "ready" || slashMCPServer.status === "degraded" ? "关闭 Server" : "启动 Server"}</button><button type="button" className={slashSelected === 1 ? "selected" : ""} onMouseEnter={() => setSlashSelected(1)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setMcpOpen(true); }}><Icon name="settings" size={14}/> 配置</button></div></div>}
          {slashPanel === "skill" && <div className="slash-runtime-list">{slashPanelLoading ? <p>正在读取项目 Skills…</p> : slashSkills.length ? slashSkills.map((skill, index) => <button type="button" className={slashSelected === index ? "selected" : ""} key={skill.name} aria-disabled={!skill.enabled} onMouseEnter={() => setSlashSelected(index)} onMouseDown={(event) => { event.preventDefault(); void executeSlashPanelSelection(index); }}><span className="slash-command-icon"><Icon name="skill" size={16}/></span><span className="slash-command-copy"><b>{skill.name} <i>{skillOriginText(skill.origin)}</i></b><small>{skill.description}</small></span><span className={`slash-command-state ${skill.enabled ? "on" : "off"}`}><i/>{skill.enabled ? "允许加载" : "已禁用"}</span></button>) : <p>当前没有可作为入口的 Skill</p>}<button type="button" className={`slash-manage ${slashSelected === slashSkills.length ? "selected" : ""}`} onMouseEnter={() => setSlashSelected(slashSkills.length)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setSkillsOpen(true); }}><Icon name="settings" size={14}/> 管理 Skills</button></div>}
          {slashPanel === "knowledge" && <div className="slash-runtime-list">{slashPanelLoading ? <p>正在读取项目资料库…</p> : slashKnowledge.length ? slashKnowledge.map((document) => <div className="slash-runtime-row" key={document.id}><span className={`slash-command-icon `}><Icon name="library" size={16}/></span><span className="slash-command-copy"><b>{document.title}</b><small>{document.originalName} · {fileSize(document.sizeBytes)}</small></span><span className={`slash-command-state ${document.status === "ready" ? "on" : document.status === "indexing" ? "loading" : "off"}`}><i/>{document.status === "ready" ? "可选作参考" : "待处理"}</span></div>) : <p>当前项目资料库为空</p>}<button type="button" className={`slash-manage ${slashSelected === 0 ? "selected" : ""}`} onMouseEnter={() => setSlashSelected(0)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setKnowledgeOpen(true); }}><Icon name="settings" size={14}/> 管理资料库</button></div>}
          {slashPanel === "model" && <div className="slash-runtime-list">{selectableModels.map(({ profile, model }, index) => { const selected = profile.id === profileId && model.id === modelId; return <button type="button" className={slashSelected === index ? "selected" : ""} key={modelKey(profile.id, model.id)} onMouseEnter={() => setSlashSelected(index)} onMouseDown={(event) => { event.preventDefault(); void executeSlashPanelSelection(index); }}><span className="slash-command-icon"><Icon name="model" size={16}/></span><span className="slash-command-copy"><b>{model.id} <i>{profile.name}</i></b><small>{modelContextSummary(model)} · {modelReasoningSummary(model).label}</small></span>{selected && <span className="slash-command-state on"><i/>当前</span>}</button>; })}<button type="button" className={`slash-manage ${slashSelected === selectableModels.length ? "selected" : ""}`} onMouseEnter={() => setSlashSelected(selectableModels.length)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setSettingsOpen(true); }}><Icon name="settings" size={14}/> 管理模型与 API</button></div>}
          {slashPanel === "reasoning" && <div className="slash-runtime-list">{reasoningLevels.map((level, index) => { const current = effectiveReasoningLevel === level; const display = reasoningDisplay(level, selectedModel, current ? activeRun : null, profileId, modelId); return <button type="button" className={slashSelected === index ? "selected" : ""} key={level} onMouseEnter={() => setSlashSelected(index)} onMouseDown={(event) => { event.preventDefault(); void executeSlashPanelSelection(index); }}><span className="slash-command-icon"><Icon name="spark" size={16}/></span><span className="slash-command-copy"><b>{level} <i>{reasoningDescriptions[level]}</i></b><small>{display.text}</small></span>{current && <span className="slash-command-state on"><i/>当前</span>}</button>; })}</div>}
          {slashPanel === "permission" && <div className="slash-runtime-list">{([{ mode: "plan" as PermissionMode, title: "Plan", description: "Workspace 内只读免确认；越界读取、写入和其他工具需确认" }, { mode: "full_access" as PermissionMode, title: "Full Access", description: "边界校验通过后，已注册工具可自动执行" }]).map((item, index) => { const current = selectedConversation?.permissionMode === item.mode; return <button type="button" className={slashSelected === index ? "selected" : ""} key={item.mode} onMouseEnter={() => setSlashSelected(index)} onMouseDown={(event) => { event.preventDefault(); void executeSlashPanelSelection(index); }}><span className="slash-command-icon"><Icon name="shield" size={16}/></span><span className="slash-command-copy"><b>{item.title}</b><small>{item.description}</small></span>{current && <span className="slash-command-state on"><i/>当前</span>}</button>; })}</div>}
          {slashPanel === "status" && <div className="slash-runtime-detail slash-status-detail">{slashPanelLoading ? <p>正在汇总当前会话状态…</p> : <>{slashStatusErrors.length > 0 && <p className="slash-status-warning">部分状态不可用：{slashStatusErrors.join("、")}</p>}<div className="slash-status-list"><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="model" size={16}/></span><span className="slash-command-copy"><b>{selectedModel?.id ?? "未选择模型"} <i>{selectedProfile?.name}</i></b><small>{selectedProfile ? protocolLabels[selectedProfile.apiProtocol] : "模型配置不可用"}</small></span><span className={`slash-command-state ${selectedProfile?.secretConfigured ? "on" : "off"}`}><i/>{selectedProfile?.secretConfigured ? "API 已配置" : "缺少 Key"}</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="refresh" size={16}/></span><span className="slash-command-copy"><b>上下文窗口 <i>{statusContextWindow.toLocaleString()} tokens</i></b><small>自动压缩阈值 {statusCompactLimit.toLocaleString()}{matchingRun ? ` · 最近输入 ${matchingRun.inputTokens.toLocaleString()}` : ""}</small></span><span className={`slash-command-state ${matchingRun?.contextCompacted ? "on" : "off"}`}><i/>{matchingRun?.contextCompacted ? "已有 checkpoint" : "未压缩"}</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="spark" size={16}/></span><span className="slash-command-copy"><b>思考强度 <i>{selectedConversation?.reasoningLevel}</i></b><small>{reasoning.text}</small></span><span className="slash-command-state on"><i/>已设置</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="shield" size={16}/></span><span className="slash-command-copy"><b>工具权限 <i>{selectedConversation?.permissionMode === "full_access" ? "Full Access" : "Plan"}</i></b><small>{selectedConversation?.permissionMode === "full_access" ? "已注册工具通过边界校验后自动执行" : "越界读取、写入和其他工具需要确认"}</small></span><span className="slash-command-state on"><i/>当前</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="server" size={16}/></span><span className="slash-command-copy"><b>MCP Servers <i>{mcpServers?.length ?? 0} 个</i></b><small>{activeMCPCount > 0 ? `${activeMCPCount} 个 Server 已连接并注册工具` : "当前没有已连接 Server"}</small></span><span className={`slash-command-state ${mcpStatus.kind}`}><i/>{mcpStatus.text}</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="skill" size={16}/></span><span className="slash-command-copy"><b>Skills <i>{enabledSkillCount}/{availableSkillCount}</i></b><small>{availableSkillCount > 0 ? "当前项目已启用 / 可用" : "当前没有可用 Skill"}</small></span><span className={`slash-command-state ${enabledSkillCount > 0 ? "on" : "off"}`}><i/>{enabledSkillCount > 0 ? "已启用" : "未启用"}</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="library" size={16}/></span><span className="slash-command-copy"><b>资料库 <i>{readyKnowledgeCount}/{slashKnowledge.length}</i></b><small>{slashKnowledge.length > 0 ? "可读取 / 已保存资料" : "当前项目资料库为空"}</small></span><span className={`slash-command-state ${readyKnowledgeCount > 0 ? "on" : slashKnowledge.some((item) => item.status === "indexing") ? "loading" : "off"}`}><i/>{readyKnowledgeCount > 0 ? "可读取" : slashKnowledge.some((item) => item.status === "indexing") ? "索引中" : "无文献"}</span></div><div className="slash-runtime-row"><span className="slash-command-icon"><Icon name="search" size={16}/></span><span className="slash-command-copy"><b>识图兜底 <i>{enabledVisionCount}/{slashVision.length}</i></b><small>{slashVision.length > 0 ? slashVision.map((item) => item.modelId).join(" → ") : "尚未配置自定义视觉渠道"}</small></span><span className={`slash-command-state ${enabledVisionCount > 0 ? "on" : "off"}`}><i/>{enabledVisionCount > 0 ? "可用" : slashVision.length > 0 ? "未启用" : "未配置"}</span></div></div></>}</div>}
          {slashPanel === "usage" && <div className="slash-runtime-detail">{slashPanelLoading ? <p>正在读取用量统计…</p> : slashUsage ? <><div className="slash-usage-grid"><span><small>实际总 Token</small><b>{slashUsage.summary.realTotalTokens.toLocaleString()}</b></span><span><small>模型请求</small><b>{slashUsage.summary.requestCount.toLocaleString()}</b></span><span><small>推理 Token</small><b>{slashUsage.summary.reasoningTokens.toLocaleString()}</b></span><span><small>缓存命中率</small><b>{slashUsage.summary.cacheDataAvailable ? `${(slashUsage.summary.cacheHitRate * 100).toFixed(1)}%` : "无数据"}</b></span></div><section><b>按模型</b>{slashUsage.models.slice(0, 6).map((item) => <div className="slash-usage-model" key={`${item.modelProfileId}:${item.modelId}`}><span>{item.profileName} · {item.modelId}</span><b>{item.realTotalTokens.toLocaleString()}</b></div>)}</section></> : <p>暂无用量数据</p>}<div className="slash-detail-actions"><button type="button" className={slashSelected === 0 ? "selected" : ""} onMouseEnter={() => setSlashSelected(0)} onMouseDown={(event) => { event.preventDefault(); setSlashPanel(null); setUsageOpen(true); }}><Icon name="chart" size={14}/> 打开完整统计</button></div></div>}
        </div>}
        <form className="composer" aria-busy={sending} onSubmit={(event) => void send(event)}>
          {pendingAttachments.length > 0 && <div className="pending-attachments">{pendingAttachments.map((item) => <div key={item.id}><span><Icon name={item.format === "image" ? "model" : "skill"} size={15}/></span><b title={item.originalName}>{item.originalName}</b><small>{attachmentSummary(item)}</small><button type="button" aria-label={`移除 ${item.originalName}`} title="移除附件" onClick={() => setPendingAttachments((current) => current.filter((value) => value.id !== item.id))}><Icon name="close" size={13}/></button></div>)}</div>}
          {pendingAttachments.some((item) => item.format === "image") && <div className="image-routing-note"><Icon name="model" size={14}/><span>图片优先由当前模型识别；API 明确拒绝图片输入后，SciAide 将按顺序调用已配置的自定义识图渠道，并在处理记录中标明实际模型。</span></div>}
          <textarea ref={composerInputRef} value={input} disabled={researchConversationLocked || Boolean(busy && activeRunIsWorkflowAI)} onChange={(event) => { setInput(event.target.value); if (slashPanel !== null) setSlashPanel(null); }} onKeyDown={(event) => {
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
          }} placeholder={!conversationId ? "输入 / 打开功能，或先创建研究会话" : researchConversationLocked ? "科研流程正在自主推进，任务结束后可继续提问…" : busy && activeRunIsWorkflowAI ? "当前科研阶段由 AI 自主执行，完成后可继续提问…" : busy ? "正在回答，可先编辑下一条消息…" : researchRevisionReady ? "结论不对？向AI提出疑问并令其返修..." : "向 SciAide 描述研究问题，或输入 / 使用命令…"}/>
          <div className="composer-actions"><span>{researchConversationLocked ? "流程与 AI 正在完成闭环；可在左侧暂停或取消任务" : busy && activeRunIsWorkflowAI ? "当前阶段会自动推进；可在左侧暂停或取消科研任务" : busy ? "可先编辑下一条消息；点击右侧停止生成" : <><kbd>Enter</kbd> 发送 · <kbd>Shift Enter</kbd> 换行</>}</span><div className="composer-buttons">{researchRevisionReady && <button type="button" className="attach" title="从资料库补充文献，确认返修后纳入研究" aria-label="从资料库补充文献" disabled={busy || sending || importingAttachments} onClick={()=>setRevisionLibraryOpen(true)}><Icon name="archive" size={17}/></button>}<button type="button" className={`web-search-toggle ${webSearchEnabled ? "enabled" : ""}`} aria-label="联网搜索" aria-pressed={webSearchEnabled} disabled={busy || sending || researchConversationLocked} title={webSearchEnabled ? "已开启：AI 可按需搜索和读取网页，不会强制搜索" : "已关闭：不向本次对话提供联网搜索与网页工具；不改变科研自动路线"} onClick={() => setWebSearchEnabled(value => !value)}><Icon name="search" size={14}/><span>联网</span></button><div className={`reasoning-picker ${reasoning.kind}`} title={`思考强度：${effectiveReasoningLevel} · ${reasoning.text}。参数已接受只代表服务端接受档位；收到 thinking/reasoning 块或 reasoning token 后才显示已验证。明确拒绝时逐级回退，不发送后台探测。`}><Icon name="spark" size={13}/><select aria-label="思考强度" value={effectiveReasoningLevel} disabled={!selectableModels.length || busy || researchConversationLocked} onChange={(event) => void changeReasoningLevel(event.target.value as ReasoningLevel)}>{reasoningLevels.map((level) => <option value={level} key={level}>{level}</option>)}</select><span className="reasoning-state">{reasoning.text.replace(`${effectiveReasoningLevel} · `, "").replace(`${effectiveReasoningLevel} `, "")}</span></div><button type="button" className="attach" aria-label="添加当前对话附件" title={researchRevisionReady ? "添加文献供 AI 核对；确认返修方案后才纳入研究" : "仅添加到当前对话，不会保存研究材料"} disabled={!conversationId || !projectId || importingAttachments || researchConversationLocked || Boolean(busy && activeRunIsWorkflowAI)} onClick={() => void attachDocuments()}><Icon name={importingAttachments ? "refresh" : "paperclip"} size={17}/></button>{busy && !activeRunIsWorkflowAI && !researchConversationLocked ? <button type="button" className="send is-stop" aria-label={cancellingRunId === activeRun?.id ? "正在停止" : "停止生成"} title={cancellingRunId === activeRun?.id ? "正在停止…" : "停止生成"} disabled={sending || !activeRun || activeRun.conversationId !== conversationId || cancellingRunId === activeRun.id} onClick={() => void stopConversationRun()}><Icon name={cancellingRunId === activeRun?.id ? "refresh" : "stop"} size={17}/></button> : <button type="submit" className="send" aria-label={researchConversationLocked || busy && activeRunIsWorkflowAI ? "科研流程执行中" : "发送"} disabled={researchConversationLocked || busy || sending || importingAttachments || !exactSlashCommand && ((!input.trim() && pendingAttachments.length === 0) || !conversationId || !profileId || !modelId)}><Icon name="send" size={17}/></button>}</div></div>
        </form>
        <p className="composer-hint">AI 可能会出错，重要科研结论请核验原始来源。</p>
      </footer>
    </main>
    {revisionLibraryOpen && researchRevisionReady && <RevisionLibraryPicker key={`${projectId}:${conversationId}`} projectId={projectId} service={backend} close={()=>setRevisionLibraryOpen(false)} choose={files=>{const merged=[...pendingAttachments,...files.filter(v=>!pendingAttachments.some(c=>c.id===v.id))];if(merged.length>20){setNotice("每条消息最多添加 20 个附件，请减少待发送文件后重新选择。");setRevisionLibraryOpen(false);return;}setPendingAttachments(merged);setRevisionLibraryOpen(false);}}/>}
    {(settingsHubOpen||settingsOpen||mcpOpen||skillsOpen||networkOpen||searchPageOpen) && <SettingsDialog close={closeSettings}><nav className="application-settings-nav" aria-label="设置分类"><div className="settings-nav-heading"><h2>设置</h2><button type="button" className="settings-close" aria-label="关闭设置" title="关闭设置 · Esc" data-dialog-dismiss onClick={closeSettings}><Icon name="close" size={16}/></button></div><div className="settings-nav-items">{([{id:"models",label:"模型与 API",icon:"settings"},{id:"search",label:"联网搜索",icon:"search"},{id:"skills",label:"Skills",icon:"skill"},{id:"mcp",label:"MCP 服务",icon:"server"},{id:"network",label:"网络与代理",icon:"shield"}] as const).map(tab=><button type="button" data-dialog-transition key={tab.id} className={(tab.id==="search"&&searchPageOpen||tab.id==="models"&&settingsOpen||tab.id==="skills"&&skillsOpen||tab.id==="mcp"&&mcpOpen||tab.id==="network"&&networkOpen)?"active":""} onClick={()=>{setSearchPageOpen(tab.id==="search");setSettingsOpen(tab.id==="models");setSkillsOpen(tab.id==="skills");setMcpOpen(tab.id==="mcp");setNetworkOpen(tab.id==="network");}}><span className="settings-nav-icon"><Icon name={tab.icon} size={17}/></span><span>{tab.label}</span></button>)}</div></nav><div className="application-settings-content">
      {settingsOpen&&<ModelSettings profiles={profiles} refresh={loadProfiles} select={setProfileId}/>}
      {mcpOpen&&<MCPSettings/>}
      {skillsOpen&&<SkillSettings project={selectedProject}/>}
      {searchPageOpen&&<SettingsPage title="联网搜索" description="配置搜索渠道与密钥，拖拽调整优先级。" className="search-settings-page"><WebSearchSettings openNetwork={()=>{setSearchPageOpen(false);setNetworkOpen(true)}}/></SettingsPage>}
      {networkOpen&&<NetworkSettings service={backend}/>}
    </div></SettingsDialog>}
    {usageOpen && <UsageDashboard profiles={profiles} close={() => setUsageOpen(false)}/>}
    {knowledgeOpen && selectedProject && <ResearchMaterialsLibrary key={`${selectedProject.id}:${resourceTaskId}`} service={backend} project={selectedProject} taskId={resourceTaskId} close={() => { setKnowledgeOpen(false); setResourceTaskId(""); }}/>}
    {pythonOpen && selectedProject && <PythonEnvironmentSettings project={selectedProject} close={() => setPythonOpen(false)} feedback={setNotice}/>}
    {artifactsOpen && selectedProject && <ArtifactLibrary project={selectedProject} taskId={resourceTaskId} close={() => { setArtifactsOpen(false); setResourceTaskId(""); }}/>}
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
    {createDialog && <CreateModal busy={creating} value={createDialog} setValue={setCreateDialog} close={() => setCreateDialog(null)} submit={submitCreate}/>}
    <AppDialogHost/>
  </div>;

  async function removeProject(value: Project) {
    const effect = value.workspaceKind === "managed" ? "托管目录会移至 ~/.sciaide/backups/trash，可手动恢复。" : "仅移除 SciAide 记录，外部目录及文件不会删除。";
    if (!await appConfirm({ title: `移除“${value.name}”？`, message: `${effect}\n项目下的会话和运行记录将删除。`, confirmLabel: "移除项目", tone: "danger" })) return;
    try { await backend("ProjectFacade", "RemoveProject", value.id); setProjectId(""); setConversationId(""); setMessages([]); await loadProjects(); setNotice("项目已从 SciAide 移除。"); } catch (error) { setNotice(errorText(error)); }
  }

  function closeSettings() {
    setSearchPageOpen(false);setSettingsHubOpen(false);setSettingsOpen(false);setSkillsOpen(false);setMcpOpen(false);setNetworkOpen(false);void loadMCPStatus();
  }

  async function exportProjectArchive(value: Project) {
    if (archiveBusy) return;
    setArchiveBusy("export");
    try {
      if (!await appConfirm({title:`导出“${value.name}”的项目备份？`,message:"将项目记录、关联资料和科研产物保存为 .sciaide-project 文件，用于备份或迁移。不会打包 Workspace 中所有文件。\n\n不包含程序保存的 API Key 等配置凭据；但备份不加密，聊天与资料仍可能含敏感信息，请谨慎分享。",confirmLabel:"选择保存位置"})) return;
      const result = await backend<ProjectArchiveExportResult>("ProjectArchiveFacade", "ExportProject", value.id);
      if (!result.path) return;
      setNotice(`项目备份已导出：${fileSize(result.sizeBytes)} · ${result.fileCount} 个文件 · SHA256 ${result.sha256.slice(0, 12)}…`);
    } catch (error) { setNotice(errorText(error)); }
    finally { setArchiveBusy(""); }
  }

  async function restoreProjectArchive() {
    if (archiveBusy) return;
    setArchiveBusy("restore");
    try {
      if (!await appConfirm({title:"从备份导入项目",message:"选择 .sciaide-project 备份文件，导入为独立的新项目，不覆盖已有项目，也不修改原备份。\n\n模型密钥不会随备份导入；继续运行前可能需要重新配置模型、工具及 Python 环境。请只导入来源可信的备份。",confirmLabel:"选择备份文件"})) return;
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
      if (updated.id === researchConversationIdRef.current) setResearchConversation(updated); else setConversations((current) => current.map((item) => item.id === updated.id ? updated : item));
      setNotice(mode === "full_access" ? "已启用 Full Access：注册工具通过边界校验后将自动执行。" : "已切换到 Plan：Workspace 内只读免确认，越界读取、写入和其他工具需要确认。");
    } catch (error) { setNotice(errorText(error)); }
  }

  async function changeReasoningLevel(level: ReasoningLevel) {
    if (!level || !reasoningLevels.includes(level) || busy) return;
    const display = reasoningDisplay(level, selectedModel, null, profileId, modelId);
    if (!settingsConversation) {
      setWorkspaceReasoningLevel(level);
      setNotice(`已设置下一次对话/科研任务的思考强度：${level}`);
      return;
    }
    if (settingsConversation.reasoningLevel === level) return;
    try {
      const updated = await backend<Conversation>("ConversationFacade", "SetReasoningLevel", settingsConversation.id, level);
      // An existing conversation override must not change defaults for new work.
      if (updated.id === researchConversationIdRef.current) setResearchConversation(updated); else setConversations((current) => current.map((item) => item.id === updated.id ? updated : item));
      setNotice(`思考强度：${display.text}`);
    } catch (error) { setNotice(errorText(error)); }
  }

  async function removeConversation(value: Conversation) {
    if (!await appConfirm({ title: `移除会话“${value.title}”？`, message: "该会话的消息和运行记录将删除，Workspace 文件不受影响。", confirmLabel: "移除会话", tone: "danger" })) return;
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

const MessageRow = memo(function MessageRow({ message, providerName, run, runActive, retryStatus, runSteps, toolCalls, approvals, revealing, resolvingApprovalId, resolveApproval, saveArtifact, researchStageTask, workflowActivity, resolveResearchApproval }: {
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
	researchStageTask?: ResearchStageTask;
	workflowActivity?: WorkflowMessageActivity;
	resolveResearchApproval?: (approval: Approval, allow: boolean) => void | Promise<void>;
}) {
  const attachments = attachmentsOf(message);
  const stagePrompt = message.role === "user" && (message.internal || Boolean(researchStageTask));
  // Internal Workflow prompts are execution metadata, not conversation turns.
  // The corresponding AI reply owns its own expandable activity record.
  if (stagePrompt) return null;
  // `internal` is persisted from the Workflow/Chat binding and remains
  // available after reopening the app. Do not rely only on the live stage map:
  // that map is transient and may not be populated yet for a restored Run.
  const workflowAIMessage = Boolean(message.internal || researchStageTask || workflowActivity);
  return <article className={`message ${message.role}${stagePrompt ? " research-stage-task" : ""}${revealing ? " revealing" : ""}`} aria-busy={revealing}>
    <div className="avatar">{stagePrompt ? <Icon name="history" size={16}/> : message.role === "user" ? "你" : <Icon name="spark" size={17}/>}</div>
    <div className="message-body">
      <div className="message-meta"><b>{stagePrompt ? "系统 · 阶段任务" : message.role === "user" ? "你" : providerName}</b>{stagePrompt && <span>{researchStageTask?.nodeName} · {researchStageTask?.promptVersion}</span>}{message.status === "incomplete" && <span>生成已中断</span>}</div>
      {attachments.length > 0 && <div className="message-attachments">{attachments.map((item) => <div className="attachment-card" key={item.attachmentId}><span><Icon name={item.format === "image" ? "model" : "skill"} size={16}/></span><div><b title={item.originalName}>{item.originalName}</b><small>{attachmentSummary(item)}</small></div></div>)}</div>}
      {message.role === "assistant" && workflowAIMessage && workflowActivity && <WorkflowMessageActivityCard activity={workflowActivity} retryStatus={retryStatus} busy={workflowActivity.busy || resolvingApprovalId} resolveApproval={resolveResearchApproval}/>}
      {message.role === "assistant" && !run && message.runId && !workflowActivity && <HistoricalRunProcess runId={message.runId} reasoning={message.reasoning} resolveApproval={resolveApproval}/>}
      {message.role === "assistant" && run && !workflowActivity && <RunProcess answerStarted={Boolean(visibleMessageText(message))} run={run} active={runActive} retryStatus={retryStatus} steps={runSteps} reasoning={message.reasoning} toolCalls={toolCalls} approvals={approvals} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>}
      <CitedAnswer message={message} revealing={revealing} saveArtifact={saveArtifact}/>
      {message.status === "sending" && <small role="status">发送中</small>}
      {message.status === "send_failed" && <small role="alert">发送失败</small>}
    </div>
  </article>;
});

function CitationMarker({ reference, citations, selectReference }: { reference: string; citations: CitationDisplayMap; selectReference: (reference: string) => void }) {
  const citation = citations.get(reference);
  return citation
    ? <button type="button" className="citation-marker" aria-label={`查看引用 ${citation.number}`} title={`${citation.value.bibliography?.data?.title || citation.value.title || citation.value.sourceName} · ${citation.value.locator || ""}`} onClick={() => selectReference(reference)}>[{citation.number}]</button>
    : <span className="citation-unverified" title="当前内容的引用快照中没有可用的对应证据">{reference}</span>;
}

function CitationEvidence({ selected, close }: { selected: DisplayCitation; close: () => void }) {
  const [showRawQuote, setShowRawQuote] = useState(false);
  const bibliography = selected.bibliography?.data;
  const url = citationSourceURL(selected);
  return <section className="citation-detail" aria-label="引用证据">
    <header><span><Icon name="library" size={14}/></span><div><b>{bibliography?.title || selected.title || selected.sourceName}</b><small>{[selected.sourceName, selected.locator].filter(Boolean).join(" · ")}</small></div><button type="button" className="citation-view-toggle" title={showRawQuote ? "恢复整理后的文本" : "查看参与证据校验的原始文本"} onClick={() => setShowRawQuote(value => !value)}>{showRawQuote ? "整理文本" : "原始文本"}</button><button type="button" aria-label="关闭引用详情" data-dialog-dismiss onClick={close}><Icon name="close" size={13}/></button></header>
    {bibliography && <p className="citation-bibliography">{bibliography.workType === "user_material" ? "用户提供资料 · 书目信息与全文完整性未核验" : [bibliography.containerTitle, bibliography.year].filter(Boolean).join(" · ")}{url && <a href={url} target="_blank" rel="noopener noreferrer">打开文献页面</a>}</p>}
    <blockquote>{showRawQuote ? selected.quote : formatCitationQuote(selected.quote)}</blockquote>
    <footer><span>引用快照</span>{selected.evidenceLevel && <small>{selected.evidenceLevel === "full_text" ? "全文片段" : selected.evidenceLevel === "metadata_abstract" ? "摘要 / 题录" : "未标注证据层级"}</small>}<code>{selected.reference}</code><small title={selected.quoteSha256}>证据 {selected.quoteSha256.slice(0, 12)}</small></footer>
  </section>;
}

function CitationDialog({ selected, close }: { selected: DisplayCitation; close: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  return createPortal(<ModalDialog dialogRef={dialog} close={close} className="citation-dialog" aria-label="引用文献与证据">
    <CitationEvidence key={`${selected.reference}:${selected.quoteSha256}`} selected={selected} close={close}/>
  </ModalDialog>, document.body);
}

function CitedDocument({ citations, children }: { citations: unknown; children: (map: CitationDisplayMap, select: (reference: string) => void) => ReactNode }) {
  const [selectedReference, setSelectedReference] = useState("");
  const map = useMemo(() => citationDisplayMap(citations), [citations]);
  const selected = map.get(selectedReference)?.value;
  return <>{children(map, setSelectedReference)}{selected && <CitationDialog selected={selected} close={() => setSelectedReference("")}/>}</>;
}

function CitedMarkdown({ text, citations }: { text: string; citations: unknown }) {
  return <CitedDocument citations={citations}>{(map, select) => <MarkdownAnswer text={text} citations={map} selectReference={select}/>}</CitedDocument>;
}

function CitationText({ text = "", citations, selectReference }: { text?: string; citations: CitationDisplayMap; selectReference: (reference: string) => void }) {
  return <>{citationTextSegments(text).map((part, index) => part.reference
    ? <CitationMarker key={index} reference={part.reference} citations={citations} selectReference={selectReference}/>
    : <Fragment key={index}>{part.text}</Fragment>)}</>;
}

function MarkdownAnswer({ text, citations, selectReference }: {
  text: string;
  citations: CitationDisplayMap;
  selectReference: (reference: string) => void;
}) {
  const live = useRef({citations, selectReference});
  live.current = {citations, selectReference};
  const citationKey = [...citations].map(([key, item]) => `${key}:${item.number}:${item.value.sourceName}:${item.value.locator}:${item.value.title}:${item.value.bibliography?.data?.title}:${item.value.quoteSha256}`).join("\n");
  return useMemo(() => <div className="markdown-body">
    <ReactMarkdown
      remarkPlugins={markdownRemarkPlugins}
      rehypePlugins={markdownRehypePlugins}
      skipHtml
      urlTransform={safeMarkdownURL}
      components={{
        pre: ({ children }) => <MarkdownCodeBlock copy={copyToClipboard}>{children}</MarkdownCodeBlock>,
        a: ({ href, children }) => {
          const reference = citationReferenceFromURL(href);
          if (reference) return <CitationMarker reference={reference} citations={live.current.citations} selectReference={value => live.current.selectReference(value)}/>;
          if (!href) return <span className="markdown-link-disabled" title="出于安全原因，此链接不可打开">{children}</span>;
          return <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>;
        },
        img: ({ alt }) => <span className="markdown-image-placeholder">[图片：{alt || "未命名"}]</span>,
      }}
    >{text}</ReactMarkdown>
  </div>, [text, citationKey]);
}

function CitedAnswer({ message, revealing = false, saveArtifact }: { message: Message; revealing?: boolean; saveArtifact: (message: Message) => Promise<void> }) {
  const [selectedReference, setSelectedReference] = useState("");
  const [copied, setCopied] = useState(false);
	const [technicalOpen, setTechnicalOpen] = useState(false);
	const [saving, setSaving] = useState(false);
  useEffect(() => setSelectedReference(""), [message.id]);
  useEffect(() => setCopied(false), [message.id]);
  const text = message.internal && message.status !== "complete" ? "" : visibleMessageText(message);
  const technical = useMemo(() => message.internal && message.role === "assistant" && message.status === "complete"
    ? researchAnswerPresentation(textOf(message), message.citations).details : "", [message]);
  const citations = [...(message.citations ?? [])].sort((left, right) => left.ordinal - right.ordinal);
  const byReference = new Map(citations.map((value, index) => [value.reference, { value, number: index + 1 }]));
  const selected = byReference.get(selectedReference)?.value;
  async function copyAnswer() {
    await copyToClipboard(text);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  }
  return <>
    <div className="bubble">{text ? message.role === "assistant" ? <MarkdownAnswer text={text} citations={byReference} selectReference={(reference) => setSelectedReference((current) => current === reference ? "" : reference)}/> : text : ""}</div>
    {technical && <details className="research-timeline-json" onToggle={event => setTechnicalOpen(event.currentTarget.open)}><summary>技术详情 · 原始阶段回复</summary>{technicalOpen && <pre>{technical}</pre>}</details>}
    {message.role === "assistant" && text && !revealing && <div className="message-actions"><button type="button" aria-label="复制回答" title={copied ? "已复制" : "复制回答"} onClick={() => void copyAnswer()}><Icon name={copied ? "check" : "copy"} size={14}/><span>{copied ? "已复制" : "复制"}</span></button>{message.status === "complete" && !message.internal && <button type="button" aria-label="保存为科研产物" title="把完整回答和引用快照保存为不可变科研产物" disabled={saving} onClick={() => { setSaving(true); void saveArtifact(message).finally(() => setSaving(false)); }}><Icon name={saving ? "refresh" : "archive"} size={14}/><span>{saving ? "保存中" : "保存产物"}</span></button>}</div>}
    {selected && <CitationEvidence key={`${message.id}:${selected.reference}`} selected={selected} close={() => setSelectedReference("")}/>}
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

type RunProcessFrameProps = {
  active: boolean;
  waiting?: boolean;
  failed?: boolean;
  statusText: string;
  duration: string;
  liveDetail?: string;
  initiallyOpen?: boolean;
  resetKey?: string;
  className?: string;
  stateClassName?: string;
  detailClassName?: string;
  detail?: ReactNode;
  empty?: boolean;
  // Callers may provide a complete status node when the label needs to stay
  // byte-for-byte compatible with an existing history projection.
  statusNode?: ReactNode;
  children?: ReactNode;
};

// Shared shell for ordinary chat Runs and Workflow AI stage Runs.  The
// content inside the expandable region remains caller-specific, but the
// status row, spinner, ordering and collapse behavior are identical.
function RunProcessFrame({ active, waiting = false, failed = false, statusText, duration, liveDetail, initiallyOpen = false, resetKey = "", className = "", stateClassName = "", detailClassName = "", detail, empty = false, statusNode, children }: RunProcessFrameProps) {
  const [open, setOpen] = useState(active || initiallyOpen);
  useEffect(() => setOpen(active || initiallyOpen), [active, initiallyOpen, resetKey]);
  const running = active && !waiting;
  const toggle = <button type="button" className="run-process-toggle" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
    <span className={`run-process-state ${stateClassName} ${running ? "spinning" : ""}`.trim()}>{running ? <span/> : <Icon name={failed ? "close" : waiting ? "shield" : "check"} size={13}/>}</span>
    {statusNode ?? <b>{statusText} {duration}</b>}
    {active && liveDetail && <small>{liveDetail}</small>}
    <i>{open ? "收起" : "展开"}</i>
  </button>;
  const body = open && <div className={`run-process-detail ${detailClassName}`.trim()}>{empty ? <p className="run-process-empty">本轮未产生工具调用或可展示的推理摘要。</p> : detail}{children}</div>;
  return <section className={`run-process ${active ? "active" : "complete"} ${waiting ? "waiting" : ""} ${className}`.trim()}>
    {!active && toggle}
    {body}
    {active && toggle}
  </section>;
}

function RunProcess({ run, active, retryStatus, steps, reasoning, toolCalls, approvals, resolvingApprovalId, resolveApproval, initiallyOpen = false, answerStarted = false }: {
  run: Run; active: boolean; steps: RunStep[]; reasoning?: MessageReasoning; toolCalls: ToolCall[]; approvals: Approval[];
  retryStatus: RetryStatus | null;
  resolvingApprovalId: string; resolveApproval: (approval: Approval, allow: boolean) => Promise<void>; initiallyOpen?: boolean; answerStarted?: boolean;
}) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  const waiting = run.status === "waiting_approval" || approvals.length > 0;
  const failed = ["failed", "cancelled", "interrupted"].includes(run.status);
  const title = waiting ? "等待确认" : active ? "处理中" : failed ? "处理已停止" : "已处理";
  const [expanded, setExpanded] = useState(initiallyOpen);
  useEffect(() => setExpanded(initiallyOpen), [run.id, initiallyOpen]);
  const currentCall = toolCalls.find(call => approvals.some(approval => approval.toolCallId === call.id))
    ?? toolCalls.find(call => call.status === "awaiting_approval")
    ?? toolCalls.find(call => call.status === "running")
    ?? toolCalls.find(call => call.status === "pending");
  const queued = toolCalls.filter(call => call.id !== currentCall?.id && ["pending", "awaiting_approval"].includes(call.status)).length;
  const currentApproval = approvals.find(approval => approval.toolCallId === currentCall?.id);
  // Current activity is independent of both answer streaming and history expansion.
  const showCurrent = active && currentCall;
  const historyCalls = showCurrent ? toolCalls.filter(call => call.id !== currentCall.id) : toolCalls;
  const liveDetail = retryStatusLabel(retryStatus) || (waiting ? "请确认当前操作后继续" : currentCall ? toolPresentation({toolName: currentCall.toolName, arguments: safeToolArguments(currentCall.arguments)}).title : answerStarted ? "正在生成回答" : "正在分析问题");
  return <section className={`run-process compact-process ${active ? "active" : "complete"}`}>
    <button type="button" className="run-process-toggle" aria-expanded={expanded} onClick={() => setExpanded(value => !value)}>
      <span className={`run-process-state ${active && !waiting ? "spinning" : ""}`}>{active && !waiting ? <span/> : <Icon name={waiting ? "shield" : failed ? "close" : "check"} size={13}/>}</span>
      <b>{title} {runDuration(run, now)}</b><small>{active ? liveDetail : `共 ${toolCalls.length} 项操作`}</small>
    </button>
    {expanded && <div className="run-process-detail">
      <ReasoningPrelude run={run} reasoning={reasoning}/>
      <RunTimeline run={run} steps={steps} toolCalls={historyCalls} approvals={approvals} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>
    </div>}
    {showCurrent && <div className="current-tool-activity">
      <ToolActivityCard key={currentCall.id} call={currentCall} approval={currentApproval} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>
      {queued > 0 && <small className="tool-queue-summary">另有 {queued} 项排队，尚未执行</small>}
    </div>}
  </section>;
}

function HistoricalRunProcess({ runId, reasoning, resolveApproval, autoOpen = false }: {
  runId: string;
  reasoning?: MessageReasoning;
  resolveApproval: (approval: Approval, allow: boolean) => Promise<void>;
  autoOpen?: boolean;
}) {
  const [snapshot, setSnapshot] = useState<RunSnapshot | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  // Loading a historical snapshot is itself the user's request to inspect
  // the run. Keep that intent when the snapshot arrives; otherwise the old
  // card would silently remain collapsed and look as if no activity existed.
  const [open, setOpen] = useState(autoOpen);

  async function load(expand = false) {
    if (loading) return;
    setLoading(true);
    setError("");
    try {
      setSnapshot(await backend<RunSnapshot>("ChatFacade", "GetRunSnapshot", runId));
      // Only an explicit inspection request (or the initial auto-open) may
      // change the user's collapsed/expanded preference. Background polling
      // must refresh the snapshot without reopening a card the user closed.
      if (expand) setOpen(true);
    } catch (value) {
      setError(errorText(value));
    } finally {
      setLoading(false);
    }
  }

  // A Workflow is a chain of Chat Runs. While its enclosing task is still
  // active, restore each completed stage's safe action summary automatically
  // so the user can see what happened after Skill loading without opening
  // every collapsed history row manually.
  useEffect(() => {
    if (autoOpen) void load(true);
  }, [autoOpen, runId]);

  const snapshotActive = Boolean(snapshot && !["completed", "failed", "cancelled", "interrupted"].includes(snapshot.run.status));
  useEffect(() => {
    if (!snapshotActive || !autoOpen) return;
    const timer = window.setInterval(() => void load(false), 700);
    return () => window.clearInterval(timer);
  }, [autoOpen, runId, snapshotActive]);

  if (snapshot) return <RunProcess run={snapshot.run} active={snapshotActive} retryStatus={null} steps={snapshot.runSteps ?? []} reasoning={reasoning} toolCalls={snapshot.toolCalls ?? []} approvals={snapshot.pendingApprovals ?? []} resolvingApprovalId="" resolveApproval={resolveApproval} initiallyOpen={open}/>;

  const stopped = ["failed", "cancelled", "interrupted"].includes(reasoning?.status ?? "");
  return <section className={`run-process ${loading ? "active" : "complete"}`}>
    <button type="button" className="run-process-toggle" aria-expanded={false} onClick={() => void load(true)} disabled={loading} title={error || "读取该历史 Run 的处理记录"}>
      <span className={`run-process-state ${loading ? "spinning" : ""}`}>{loading ? <span/> : <Icon name={stopped ? "close" : "check"} size={13}/>}</span>
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
  if (providerSummary) detail = normalizeDisplayText(providerSummary);
  const title = active && !observed && !providerSummary ? "正在思考" : "推理摘要";
  return <section className={`reasoning-prelude ${active ? "active" : observed ? "observed" : "unverified"}`} aria-label={title}>
    <span className="reasoning-prelude-icon"><Icon name="spark" size={14}/></span>
    <div><header><b>{title}</b><small>{levelText}</small></header><p>{normalizeDisplayText(detail)}</p><footer>{reasoningTokens > 0 && <span>{reasoningTokens.toLocaleString()} 推理 Token</span>}{providerSummary && <span>供应商摘要</span>}{signatureObserved && <span>签名已验证</span>}</footer></div>
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
    ? <article className={`run-step ${item.step.commentary?.startsWith("[识图兜底]") ? "multimodal-fallback" : ""}`} key={`step-${item.step.turnIndex}`}><header><span>{item.step.turnIndex}</span><b>{item.step.commentary?.startsWith("[识图兜底]") ? "识图兜底" : "模型处理"}</b></header>{item.step.reasoningSummary && item.step.reasoningSummary !== run.reasoningSummary && <p>{normalizeDisplayText(item.step.reasoningSummary)}</p>}{item.step.commentary && <p>{normalizeDisplayText(item.step.commentary)}</p>}</article>
    : <ToolActivityCard key={item.call.id} call={item.call} approval={approvals.find((approval) => approval.toolCallId === item.call.id)} resolvingApprovalId={resolvingApprovalId} resolveApproval={resolveApproval}/>)}
  </section>;
}

type UnifiedToolActivity = {
  id: string;
  toolName: string;
  status: string;
  risk?: string;
  summary?: string;
  outputSummary?: string;
  errorMessage?: string;
  arguments?: unknown;
  permissions?: PermissionRequirement[];
  result?: ToolResult;
  approval?: Approval;
  resolvingApprovalId?: string;
  resolveApproval?: (approval: Approval, allow: boolean) => void | Promise<void>;
  stdoutTail?: string;
  stderrTail?: string;
  stdoutBytes?: number;
  stderrBytes?: number;
  processId?: number;
  durationMillis?: number;
  truncated?: boolean;
  source?: "chat" | "workflow";
};

function UnifiedToolActivityCard({ activity }: { activity: UnifiedToolActivity }) {
  const running = ["pending", "awaiting_approval", "running"].includes(activity.status);
  const [open, setOpen] = useState(Boolean(activity.approval));
  const displayArguments = safeToolArguments(activity.arguments);
  const presentation = toolPresentation({ toolName: activity.toolName, arguments: displayArguments, result: activity.result });
  const localExecution = activity.toolName === "builtin.shell.execute" || activity.toolName === "builtin.python.execute";
  const localExecutionSummary = localExecution ? summarizeLocalExecution({ toolName: activity.toolName, arguments: displayArguments } as ToolCall) : "";
  const argumentsPresent = displayArguments !== undefined;
  const argumentText = argumentsPresent ? JSON.stringify(displayArguments, null, 2) : "";
  const permissions = activity.permissions ?? [];
  const resultSummary = activity.outputSummary || toolResultSummary({
    toolName: activity.toolName,
    status: activity.status,
    errorMessage: activity.errorMessage,
    result: activity.result,
  });
  const failed = ["failed", "error", "denied", "cancelled", "interrupted"].includes(activity.status) || Boolean(activity.errorMessage);
  const duration = activityDurationLabel(activity.toolName, activity.durationMillis);
  useEffect(() => {
    setOpen(Boolean(activity.approval?.id));
  }, [activity.approval?.id]);
  return <article className={["tool-card", "unified-tool-card", activity.status, activity.source || ""].join(" ")}>
    <button type="button" className="tool-card-toggle" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
      <span className={["tool-icon", presentation.kind.toLowerCase(), running ? "spinning" : ""].join(" ")}>{running ? <span/> : <Icon name={presentation.icon} size={14}/>}</span>
      <span className="tool-card-heading"><span><b><em>{presentation.kind}</em><strong>{normalizeDisplayText(activity.summary || presentation.title)}</strong></b></span><span className="tool-card-state">{activity.truncated && <i>{activityTruncationLabel(activity.toolName)}</i>}{(running || failed || activity.status === "awaiting_approval") && <i className={activity.status}>{toolStatusText[activity.status] ?? activity.status}</i>}{duration && <small>{duration}</small>}</span></span>
      <Icon name={open ? "back" : "plus"} size={12}/>
    </button>
    {open && <div className="tool-card-body">
      <code className="tool-card-name">{normalizeDisplayText(activity.toolName)}</code>
      {presentation.summary && !activity.summary && <p className="tool-card-summary">{presentation.summary}</p>}
      {activity.approval && <div className="approval-panel"><div><b>{localExecution ? "确认本机进程执行" : "需要你的确认"}</b>{localExecutionSummary && <code>{localExecutionSummary}</code>}<p>{localExecution ? "命令会在当前项目 Workspace 中运行。这是受审计的本机执行器，不是强安全沙箱；接受前请核对完整参数。" : "Plan 模式下，本次工具调用只有在接受后才会执行。请核对操作内容后再决定是否允许。"}</p></div><div className="approval-actions"><button disabled={Boolean(activity.resolvingApprovalId)} onClick={() => activity.resolveApproval && void activity.resolveApproval(activity.approval!, false)}>拒绝</button><button className="accept" disabled={Boolean(activity.resolvingApprovalId)} onClick={() => activity.resolveApproval && void activity.resolveApproval(activity.approval!, true)}>{activity.resolvingApprovalId === activity.approval.id ? "处理中…" : "接受"}</button></div></div>}
      {activity.stdoutTail || activity.stderrTail ? <pre className="tool-live-output">{[activity.stdoutTail && "stdout · " + (activity.stdoutBytes ?? 0) + " bytes\n" + normalizeDisplayText(activity.stdoutTail), activity.stderrTail && "stderr · " + (activity.stderrBytes ?? 0) + " bytes\n" + normalizeDisplayText(activity.stderrTail)].filter(Boolean).join("\n")}</pre> : null}
      {!activity.stdoutTail && !activity.stderrTail && (activity.stdoutBytes || activity.stderrBytes) ? <p className="tool-live-counter">已产生输出 · stdout {activity.stdoutBytes ?? 0} bytes · stderr {activity.stderrBytes ?? 0} bytes</p> : null}
      {resultSummary && <div className={"tool-result " + (failed ? activity.status : "completed")}><span title={normalizeDisplayText(resultSummary)}>{normalizeDisplayText(resultSummary)}</span></div>}
      {activity.errorMessage && !resultSummary.includes(activity.errorMessage) && <p className="tool-card-error">{normalizeDisplayText(activity.errorMessage)}</p>}
      {(argumentsPresent || permissions.length > 0 || activity.processId) && <details className="tool-technical"><summary>技术详情</summary>{activity.processId && <p className="tool-process-id">PID {activity.processId}</p>}{argumentsPresent && <pre>{argumentText}</pre>}{permissions.length > 0 && <div className="permission-list">{permissions.map((permission) => <span key={permission.kind + ":" + permission.resource}><b>{permission.kind}</b>{permission.resource || "全部资源"}</span>)}</div>}</details>}
    </div>}
  </article>;
}

function ToolActivityCard({ call, approval, resolvingApprovalId, resolveApproval }: { call: ToolCall; approval?: Approval; resolvingApprovalId: string; resolveApproval: (approval: Approval, allow: boolean) => Promise<void> }) {
  return <UnifiedToolActivityCard activity={{ ...call, approval, resolvingApprovalId, resolveApproval, source: "chat", permissions: call.permissions, truncated: call.result?.truncated, durationMillis: call.result?.meta?.durationMillis }}/>;
}

function summarizeLocalExecution(call: Pick<ToolCall, "toolName" | "arguments">): string {
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

function ResearchChatEmpty({ hasProfile, openSettings, setPrompt }: { hasProfile: boolean; openSettings: () => void; setPrompt: (value: string) => void }) {
  const prompts = [
    "解释当前研究阶段的目标、输入和完成条件",
    "检查当前证据是否足以支持下一步，并指出缺口",
    "根据当前阶段制定下一组可执行动作",
  ];
  return <div className="research-chat-empty"><span><Icon name="spark" size={23}/></span><p>AI RESEARCH COLLABORATION</p><h1>科研协作区</h1><small>这个会话与当前科研任务一一绑定。AI 能读取可信的阶段状态，使用本阶段开放的工具，并把动作、错误和结果实时显示在这里。</small>{!hasProfile ? <button type="button" onClick={openSettings}><Icon name="model" size={15}/>选择协作模型</button> : <div>{prompts.map((prompt) => <button type="button" key={prompt} onClick={() => setPrompt(prompt)}><Icon name="chat" size={14}/><span>{prompt}</span></button>)}</div>}</div>;
}

function CreateModal({ value, setValue, close, submit, busy }: { busy:boolean; value: Exclude<CreateDialog, null>; setValue: (value: CreateDialog) => void; close: () => void; submit: (event: FormEvent) => void }) {
  async function chooseWorkspace() { try { const path = await backend<string>("ProjectFacade", "ChooseWorkspaceDirectory"); if (path) setValue({ ...value, workspacePath: path }); } catch { /* cancelled dialogs are harmless */ } }
  return <ModalBackdrop className="modal-backdrop compact" close={close} busy={busy} dirty={Boolean(value.title||value.description||value.workspacePath)}><form role="dialog" aria-modal="true" className="create-modal" onSubmit={submit}><header><span className="dialog-icon"><Icon name="folder"/></span><div><h2>新建科研项目</h2><p>集中管理一个研究方向下的会话与产物</p></div><button type="button" className="close" data-dialog-dismiss onClick={close}><Icon name="close"/></button></header><label>项目名称<input autoFocus value={value.title} onChange={(event) => setValue({ ...value, title: event.target.value })} placeholder="例如：单细胞转录组研究" maxLength={120} required/></label><><label>简要说明 <span>可选</span><textarea value={value.description} onChange={(event) => setValue({ ...value, description: event.target.value })} placeholder="记录研究目标或背景…" maxLength={500}/></label><label>Workspace 目录 <span>留空则保存到 ~/.sciaide/data/workspaces</span><div className="path-picker"><input value={value.workspacePath} onChange={(event) => setValue({ ...value, workspacePath: event.target.value })} placeholder="使用 SciAide 默认托管目录"/><button type="button" onClick={() => void chooseWorkspace()}><Icon name="folder" size={15}/> 选择文件夹</button></div></label></><footer><button type="button" data-dialog-dismiss onClick={close}>取消</button><button className="primary" disabled={busy}>{busy?"创建中…":"创建"}</button></footer></form></ModalBackdrop>;
}

function ProjectArchiveReport({ report, close, openModels, openSkills }: { report: ProjectArchiveRestoreReport; close: () => void; openModels: () => void; openSkills: () => void }) {
  const stats = [
    ["新项目", report.project.name],
    ["导入文件", report.filesRestored.toLocaleString()],
    ["导入数据", fileSize(report.bytesRestored)],
    ["Skill", `${report.restoredSkillBindings} 已匹配 / ${report.missingSkillBindings.length} 缺失`],
  ];
  return <ModalBackdrop className="modal-backdrop compact" close={close}><section className="project-archive-report" role="dialog" aria-modal="true" aria-labelledby="project-archive-report-title">
    <header><span><Icon name="archive" size={20}/></span><div><h2 id="project-archive-report-title">项目备份已导入</h2><small>已创建新的 SciAide 托管项目，原项目和备份文件未被修改。</small></div><button type="button" aria-label="关闭导入报告" data-dialog-dismiss onClick={close}><Icon name="close" size={17}/></button></header>
    <div className="archive-report-summary">{stats.map(([label, value]) => <div key={label}><span>{label}</span><b title={value}>{value}</b></div>)}</div>
    <section className="archive-security-note"><Icon name="shield" size={17}/><div><b>不导入程序保存的配置凭据</b><p>API Key、模型 Header、MCP 配置与 Secret、识图渠道、Embedding 配置、权限授权和临时缓存均未恢复。历史会话保留模型身份，但配置为禁用占位且权限回到 Plan。</p></div></section>
    {report.secretsRequireRebinding && <section className="archive-action-row"><div><b>{report.historicalProfiles} 个历史模型身份需要重新绑定</b><small>历史记录可查看；继续对话前请在“模型与 API”选择或新建可用配置。</small></div><button type="button" onClick={openModels}><Icon name="model" size={14}/>打开模型配置</button></section>}
    {report.missingSkillBindings.length > 0 && <section className="archive-missing-skills"><header><div><b>缺少完全匹配的 Skill 包</b><small>归档只保存版本和哈希绑定，不携带 Skill 源码。</small></div><button type="button" onClick={openSkills}><Icon name="skill" size={14}/>管理 Skills</button></header><div>{report.missingSkillBindings.map((item) => <span key={`${item.skillId}:${item.version}`}><code>${item.skillId}</code><b>v{item.version}</b><small>Content {item.contentHash.slice(0, 10)}… · Package {item.packageHash.slice(0, 10)}…</small></span>)}</div></section>}
    <footer><span><Icon name="check" size={14}/> 项目关系和文件 SHA256 已验证后发布</span><button type="button" data-dialog-dismiss onClick={close}>进入新项目</button></footer>
  </section></ModalBackdrop>;
}

const compactDate = (value: string) => new Date(value).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
const artifactKindText: Record<ArtifactKind, string> = { document: "文档", data: "数据", image: "图片", code: "代码", other: "文件" };
const artifactSourceText: Record<ArtifactVersion["sourceKind"], string> = { assistant_message: "助手回答", workspace_file: "Workspace 文件", tool: "工具产物" };
const artifactOriginLabel = (value: ResearchArtifact) => {
  const source = value.currentVersion?.sourceKind;
  if (source === "tool") return value.scopeKind === "task" ? "流程产出" : "工具登记";
  if (source === "assistant_message") return value.scopeKind === "task" ? "AI 阶段回答" : "助手回答";
  if (source === "workspace_file") return value.scopeKind === "task" ? "手动登记" : "手动导入";
  return "来源未知";
};
const attachmentOriginLabel = (value: { sourceKind?: string; scopeKind?: ResourceScope }) => {
  if (value.sourceKind === "research_import") return "文献导入";
  if (value.sourceKind === "conversation_upload" || value.scopeKind === "conversation") return "会话临时";
  if (value.sourceKind === "user_import") return value.scopeKind === "task" ? "手动导入" : "项目共享导入";
  return "历史来源未知";
};
const artifactCitationStyleText: Record<ArtifactCitationStyle, string> = { gb_t_7714_2015: "GB/T 7714-2015", apa_7: "APA 7" };
const artifactCanExport = (value: ArtifactVersion) => {
  const mimeType = value.mimeType.toLowerCase().split(";", 1)[0]?.trim() ?? "";
  return mimeType.startsWith("text/") || ["application/json", "application/xml", "application/yaml", "application/x-yaml", "application/javascript", "application/pdf"].includes(mimeType) || mimeType.endsWith("+json") || mimeType.endsWith("+xml") || mimeType.includes("wordprocessingml") || mimeType.includes("spreadsheetml");
};

function resourceScopeKey(value: { scopeKind?: ResourceScope; researchTaskId?: string }): string {
  if (value.scopeKind === "task" && value.researchTaskId?.trim()) return `task:${value.researchTaskId.trim()}`;
  if (value.scopeKind === "project_shared") return "project_shared";
  return "legacy_project";
}

function buildResourceScopes<T extends { scopeKind?: ResourceScope; researchTaskId?: string }>(values: T[], tasks: ResearchTask[], taskId = ""): ResourceScopeEntry[] {
  const scopedTaskId = taskId.trim();
  const count = (key: string) => values.filter((item) => resourceScopeKey(item) === key).length;
  const titleByTask = new Map(tasks.map((task) => [task.id, task.title.trim() || `任务 ${task.id.slice(0, 8)}`]));
  const observedTaskIds = values
    .filter((item) => item.scopeKind === "task" && item.researchTaskId?.trim())
    .map((item) => item.researchTaskId!.trim());
  const taskIds = [...new Set([...tasks.map((task) => task.id), ...observedTaskIds, ...(scopedTaskId ? [scopedTaskId] : [])])];
  const taskEntries = taskIds
    .filter((id) => !scopedTaskId || id === scopedTaskId)
    .map((id) => ({ key: `task:${id}`, kind: "task" as const, taskId: id, title: titleByTask.get(id) ?? `任务 ${id.slice(0, 8)}`, count: count(`task:${id}`) }))
    .filter((entry) => entry.count > 0);
  if (scopedTaskId) {
    return [
      ...taskEntries,
      { key: "project_shared", kind: "project_shared", title: "项目共享", count: count("project_shared") },
    ];
  }
  return [
    { key: "project_shared", kind: "project_shared", title: "项目共享", count: count("project_shared") },
    { key: "legacy_project", kind: "legacy_project", title: "历史未归属", count: count("legacy_project") },
    ...taskEntries,
  ];
}

function ResourceScopeNav({ entries, selectedKey, onSelect }: { entries: ResourceScopeEntry[]; selectedKey: string; onSelect: (key: string) => void }) {
  return <aside className="resource-scope-nav" aria-label="资源作用域">
    <header><b>资源归属</b><span>{entries.reduce((total, item) => total + item.count, 0)}</span></header>
    <div>{entries.map((entry) => <button type="button" className={selectedKey === entry.key ? "selected" : ""} key={entry.key} onClick={() => onSelect(entry.key)}>
      <span className={`resource-scope-icon ${entry.kind}`}><Icon name={entry.kind === "project_shared" ? "library" : entry.kind === "legacy_project" ? "history" : "folder"} size={15}/></span>
      <span><b title={entry.title}>{entry.title}</b><small>{entry.kind === "project_shared" ? "跨任务复用" : entry.kind === "legacy_project" ? "待整理的旧资源" : "科研任务"}</small></span>
      <i>{entry.count}</i>
    </button>)}</div>
  </aside>;
}

function ResourceTreeFlow({ title, columns, emptyText, actions }: { title: string; columns: ResourceTreeColumn[]; emptyText: string; actions?: ReactNode }) {
  const itemCount = columns.reduce((total, column) => total + column.items.length, 0);
  return <section className="resource-tree-pane">
    <header><div><b>{title}</b><small>按资源性质并列浏览，栏目不代表先后关系</small></div><section className="resource-tree-actions">{actions}<span>{itemCount} 个资源节点</span></section></header>
    <div className="resource-tree-scroll"><div className={`resource-tree-flow columns-${columns.length}`}>{columns.map((column) => <section className="resource-tree-column" key={column.key}>
      <header><i/><div><b>{column.title}</b><small>{column.subtitle}</small></div></header>
      <div>{column.items.length ? column.items : <p>{emptyText}</p>}</div>
    </section>)}</div></div>
  </section>;
}

function artifactTreeStage(value: ResearchArtifact): "source" | "process" | "result" {
  const version = value.currentVersion;
  const name = `${value.name} ${version?.fileName ?? ""}`.toLocaleLowerCase();
  if (version?.sourceKind === "workspace_file") return "source";
  if (/(report|deliverable|manuscript|finding|conclusion|summary|result|figure|plot|chart|design|protocol|报告|结论|结果|图表|设计稿|交付)/i.test(name)) return "result";
  if (version?.sourceKind === "assistant_message") return "result";
  return "process";
}

const researchReviewText: Record<ResearchReviewStatus, string> = { pending: "待筛选", included: "已纳入", excluded: "已排除" };
const researchImportText: Record<ResearchImportStatus, string> = { not_imported: "未保存材料", importing: "正在保存", imported: "已保存材料", failed: "保存失败" };
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

function BibliographyEvidenceWorkspace({ project, candidate, taskId = "", sourceNames, close, feedback }: { project: Project; candidate: ResearchCandidate; taskId?: string; sourceNames: Record<string, string>; close: () => void; feedback: (value: string) => void }) {
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
  useDialogGuard({busy:Boolean(busy),dirty:Boolean(evidenceContent||reason)||(!!bibliography && JSON.stringify(draft)!==JSON.stringify(bibliographyForm(bibliography.data)))});
  const scopedTaskId = taskId.trim();
  const scoped = scopedTaskId.length > 0;

  const reload = useCallback(async () => {
    const [nextBibliography, nextEvidence] = await Promise.all([
      backend<ResearchBibliography>("ResearchFacade", scoped ? "GetBibliographyForTask" : "GetBibliography", ...(scoped ? [project.id, candidate.id, scopedTaskId] : [project.id, candidate.id])),
      backend<ResearchEvidenceEntry[]>("ResearchFacade", scoped ? "ListEvidenceForTask" : "ListEvidence", ...(scoped ? [project.id, candidate.id, scopedTaskId] : [project.id, candidate.id])),
    ]);
    setBibliography(nextBibliography); setDraft(bibliographyForm(nextBibliography.data)); setEvidence(nextEvidence);
  }, [candidate.id, project.id, scoped, scopedTaskId]);

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
      const values = await backend<ResearchEvidenceSearchMatch[]>("ResearchFacade", scoped ? "SearchEvidenceForTask" : "SearchEvidence", ...(scoped ? [project.id, candidate.id, scopedTaskId, evidenceQuery.trim()] : [project.id, candidate.id, evidenceQuery.trim()]));
      setEvidenceMatches(values); if (values.length === 0) feedback("该文献的本地知识 Chunk 中没有命中。请调整检索词。");
    } catch (error) { feedback(errorText(error)); } finally { setBusy(""); }
  }

  async function saveEvidence() {
    if (!evidenceContent.trim() || busy) return;
    if ((evidenceField !== "note" || evidenceProvenance === "model") && !selectedEvidence) { feedback(evidenceProvenance === "model" ? "模型生成的条目必须先绑定该文献的本地证据 Chunk。" : "研究事实必须先绑定该文献的本地证据 Chunk。"); return; }
    setBusy("evidence-save");
    try {
      await backend<ResearchEvidenceEntry>("ResearchFacade", "SaveEvidence", { projectId: project.id, candidateId: candidate.id, researchTaskId: scopedTaskId || undefined, field: evidenceField, content: evidenceContent.trim(), provenance: evidenceProvenance, reviewStatus: evidenceReview, reference: selectedEvidence?.reference });
      setEvidenceContent(""); setSelectedEvidence(null); setEvidenceMatches([]); setEvidenceQuery(""); await reload(); feedback("证据矩阵条目已保存并完成本地 Chunk 校验。");
    } catch (error) { feedback(errorText(error)); } finally { setBusy(""); }
  }

  async function deleteEvidence(id: string) {
    if (busy) return; setBusy(`delete:${id}`);
    try { await backend<void>("ResearchFacade", scoped ? "DeleteEvidenceForTask" : "DeleteEvidence", ...(scoped ? [project.id, candidate.id, scopedTaskId, id] : [project.id, candidate.id, id])); setEvidence((current) => current.filter((item) => item.id !== id)); feedback("证据矩阵条目已删除。"); }
    catch (error) { feedback(errorText(error)); } finally { setBusy(""); }
  }

  async function reviewEvidence(id: string, reviewStatus: ResearchEvidenceEntry["reviewStatus"]) {
    if (busy) return; setBusy(`review:${id}`);
    try {
      const updated = await backend<ResearchEvidenceEntry>("ResearchFacade", scoped ? "ReviewEvidenceForTask" : "ReviewEvidence", { projectId: project.id, candidateId: candidate.id, researchTaskId: scopedTaskId || undefined, evidenceId: id, reviewStatus });
      setEvidence((current) => current.map((item) => item.id === id ? updated : item));
      feedback(reviewStatus === "verified" ? "证据条目已核验。" : reviewStatus === "rejected" ? "证据条目已拒绝，原始证据快照仍保留。" : "证据条目已重新标记为待复核。");
    } catch (error) { feedback(errorText(error)); } finally { setBusy(""); }
  }

  const fieldInput = (field: keyof ResearchBibliographyData, label: string, wide = false) => <label className={wide ? "wide" : ""}><span>{label}</span><input value={String(draft[field] ?? "")} onChange={(event) => changeField(field, field === "year" ? Math.max(0, Number(event.target.value) || 0) : event.target.value)} /></label>;
  if (loading || !bibliography) return <div className="research-evidence-workspace"><header><button type="button" data-dialog-transition onClick={close}><Icon name="back" size={14}/>返回候选</button><b>正在读取规范书目…</b></header></div>;
  const primaryEvidenceLevel = bibliography.materials.some((item) => item.evidenceLevel === "full_text") ? "full_text" : bibliography.materials[0]?.evidenceLevel;

  return <div className="research-evidence-workspace">
    <header><button type="button" data-dialog-transition onClick={close}><Icon name="back" size={14}/>返回候选</button><div><b>{candidate.preferred.title}</b><small>规范书目修订 {bibliography.revision} · {bibliography.materials.length} 个本地材料</small></div><span className={primaryEvidenceLevel ?? "none"}>{primaryEvidenceLevel ? researchEvidenceLevelText[primaryEvidenceLevel] : "尚未导入本地材料"}</span></header>
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

function ResearchDiscovery({ project, taskId = "", close, openKnowledge }: { project: Project; taskId?: string; close: () => void; openKnowledge: (taskId?: string) => void }) {
  const [materialView,setMaterialView] = useState<{taskId:string;attachmentId:string}|null>(null);
  const [selectedMaterial,setSelectedMaterial] = useState<ResearchMaterial|null>(null);
  const [materialRefresh,setMaterialRefresh] = useState(0);
  const [checkedQueries,setCheckedQueries] = useState<string[]>([]);
  const [checkedCandidates,setCheckedCandidates] = useState<string[]>([]);
  const [historyOffset, setHistoryOffset] = useState(0);
  const [historyRevision, setHistoryRevision] = useState(0);
  const [originEntries, setOriginEntries] = useState<{id: string; title: string; count: number; createdAt?: string}[]>([]);
  async function readHistory(origin: string, offset: number) {
    return backend<ResearchQuery[]>("ResearchFacade","QueryHistory",{projectId:project.id,origin,offset,limit:50});
  }
  async function deleteHistory(ids: string[], wholeGroup = false) {
    if ((!ids.length && !wholeGroup) || discoveryBusy) return;
    const count = wholeGroup ? originEntries.find((entry) => entry.id === originFilter)?.count || 0 : ids.length;
    setBusyAction("delete");
    try {
      if (!await appConfirm({title:`删除 ${count} 条检索记录？`,message:"已保存的研究材料、资料库收藏和科研产物不会删除。",confirmLabel:"删除记录",tone:"danger"})) return;
      await backend<void>("ResearchFacade","DeleteQueryHistory",{projectId:project.id,...(wholeGroup ? {origin:originFilter} : {queryIds:ids})});
      candidateRequests.current.invalidate();
      setCheckedQueries([]);setQueryId("");setCandidateId("");setEvidenceCandidateId("");
      setHistoryOffset(0);setHistoryRevision((value) => value + 1);
      setFeedback(`已删除 ${count} 条检索记录。`);
    } catch(error) {
      setFeedback(errorText(error));
      setHistoryRevision((value) => value + 1);setCheckedQueries([]);
    }
    finally {setBusyAction("");}
  }
  const [originFilter, setOriginFilter] = useState(taskId.trim() || "all");
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
  const visibleQueries = queries.filter((q) => originFilter === "all" || (originFilter === "manual" ? !q.researchTaskId : q.researchTaskId === originFilter));
  const visibleCheckedQueries = checkedQueries.filter((id) => visibleQueries.some((q) => q.id === id));
  const allVisibleChecked = visibleQueries.length > 0 && visibleCheckedQueries.length === visibleQueries.length;
  const discoveryBusy = loading || searching || Boolean(busyAction);
  function selectOrigin(id: string) {
    candidateRequests.current.invalidate();
    setOriginFilter(id); setQueryId(""); setCandidateId(""); setEvidenceCandidateId(""); setCheckedQueries([]);
    setHistoryOffset(0);setQueries([]);setLoading(true);
    setPage({ items: [], total: 0, offset: 0, limit: 20 });
  }
  function selectQuery(id: string) {
    candidateRequests.current.invalidate();
    setQueryId(id); setCandidateId(""); setEvidenceCandidateId("");
    setPage({ items: [], total: 0, offset: 0, limit: 20 });
  }
  const effectiveQueryId = visibleQueries.some((q) => q.id === queryId) ? queryId : visibleQueries[0]?.id || "";
  const scopedTaskId = queries.find((q) => q.id === effectiveQueryId)?.researchTaskId || "";

  const loadCandidates = useCallback(async (offset = 0) => {
    const request = candidateRequests.current.begin();
    if (!effectiveQueryId) { setPage({ items: [], total: 0, offset: 0, limit: 20 }); setCandidateId(""); return; }
    try {
      const values = await backend<ResearchCandidatePage>("ResearchFacade", "ListCandidates", { projectId: project.id, queryId: effectiveQueryId, status: statusFilter, search: filterText, sort, offset, limit: 20, researchTaskId: scopedTaskId || undefined });
      if (!candidateRequests.current.isCurrent(request)) return;
      setPage(values);
      setCandidateId((current) => values.items.some((item) => item.id === current) ? current : first(values.items)?.id ?? "");
    } catch (error) {
      if (candidateRequests.current.isCurrent(request)) throw error;
    }
  }, [filterText, project.id, effectiveQueryId, scopedTaskId, sort, statusFilter]);

  useEffect(() => {
    let active = true;
    setLoading(true);
    candidateRequests.current.invalidate();setPage({items:[],total:0,offset:0,limit:20});setCheckedCandidates([]);
    Promise.all([backend<ResearchSource[]>("ResearchFacade", "Catalog"), readHistory(originFilter, historyOffset), backend<{id: string; title: string; count: number}[]>("ResearchFacade", "QueryOrigins", project.id)]).then(([catalog, history, origins]) => {
      if (!active) return;
      setSources(catalog); setSelectedSources((current) => current.length ? current : catalog.map((item) => item.id)); setQueries(history); setQueryId((current) => history.some((item) => item.id === current) ? current : first(history)?.id ?? "");setOriginEntries(origins);
    }).catch((error: unknown) => active && setFeedback(errorText(error))).finally(() => active && setLoading(false));
    return () => { active = false; };
  }, [project.id, originFilter, historyOffset, historyRevision]);
  useEffect(() => {
    if (!loading) void loadCandidates().catch((error: unknown) => setFeedback(errorText(error)));
    return () => { candidateRequests.current.invalidate(); };
  }, [loadCandidates, loading]);

  const selected = page.items.find((item) => item.id === candidateId);
  useEffect(()=>{
    let active=true;setSelectedMaterial(null);
    if(selected?.attachmentId) void backend<ResearchMaterial[]>("KnowledgeFacade","ListMaterials",project.id,scopedTaskId).then(items=>{
      if(active)setSelectedMaterial(items.find(item=>item.id===selected.attachmentId)??null);
    }).catch(()=>{});
    return ()=>{active=false};
  },[project.id,scopedTaskId,selected?.attachmentId,materialRefresh]);

  async function collectCandidate() {
    if(!selected || discoveryBusy)return;
    setBusyAction("collect");setFeedback("");
    try {
      const result=await backend<ResearchMaterial>("ResearchFacade","CollectCandidate",project.id,selected.id,scopedTaskId);
      setFeedback(`已将“${result.title}”加入资料库。${result.contentKind==="metadata_abstract"?"保存的是题录/摘要，不是全文。":""}不会改变本任务的纳入或排除状态。`);
    }catch(error){setFeedback(errorText(error));}
    finally{setBusyAction("");setMaterialRefresh(value=>value+1);}
  }
  useEffect(() => {setCheckedCandidates([]);},[effectiveQueryId,page.offset,statusFilter,filterText,sort]);
  const evidenceCandidate = page.items.find((item) => item.id === evidenceCandidateId);
  const activeQuery = queries.find((item) => item.id === effectiveQueryId);
  const historyReadOnly = Boolean(activeQuery?.taskDeleted || scopedTaskId && activeQuery?.legacySnapshot);
  useEffect(() => { setExclusionReason(selected?.exclusionReason ?? ""); setNote(selected?.note ?? ""); }, [selected?.id, selected?.exclusionReason, selected?.note]);

  async function runSearch(event: FormEvent) {
    event.preventDefault();
    if (!queryText.trim() || selectedSources.length === 0 || discoveryBusy) return;
    setSearching(true); setFeedback("");
    try {
      const result = await backend<ResearchSearchResult>("ResearchFacade", "Search", { projectId: project.id, query: queryText.trim(), sourceIds: selectedSources, limit: 20 });
      setOriginFilter("manual");
      setHistoryOffset(0);setHistoryRevision((value) => value + 1);
      setCheckedQueries([]); setEvidenceCandidateId("");
      candidateRequests.current.invalidate();
      setQueries((current) => [result.query, ...current.filter((item) => item.id !== result.query.id)]);
      setQueryId(result.query.id); setPage(result.page); setCandidateId(first(result.page.items)?.id ?? "");
      const failures = result.query.sources.filter((item) => item.status === "failed");
      setFeedback(failures.length ? `检索已保存，${failures.length} 个来源失败，其余结果仍可筛选。` : `已保存 ${result.query.resultCount} 条来源记录。`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setSearching(false); }
  }

  async function updateReview(status: ResearchReviewStatus) {
    if (!selected || discoveryBusy || historyReadOnly) return;
    if (status === "excluded" && !exclusionReason.trim()) { setFeedback("排除候选时需要填写排除原因。"); return; }
    setBusyAction(`review:${status}`); setFeedback("");
    try {
      await backend<ResearchCandidate>("ResearchFacade", "UpdateReview", { projectId: project.id, candidateId: selected.id, status, exclusionReason, note, researchTaskId: scopedTaskId || undefined });
      await loadCandidates(page.offset);
      setFeedback(status === "included" ? "候选已纳入研究范围，可保存为研究材料；不会自动加入资料库。" : status === "excluded" ? "候选已排除，原因已保存。" : "候选已恢复为待筛选。");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function saveNote() {
    if (!selected || discoveryBusy || historyReadOnly) return;
    setBusyAction("note");
    try {
      await backend<ResearchCandidate>("ResearchFacade", "UpdateReview", { projectId: project.id, candidateId: selected.id, status: selected.reviewStatus, exclusionReason, note, researchTaskId: scopedTaskId || undefined });
      await loadCandidates(page.offset);
      setFeedback("筛选笔记已保存。");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function importCandidate(mode: "auto" | "full_text" | "metadata_abstract") {
    if (!selected || selected.reviewStatus !== "included" || discoveryBusy || historyReadOnly) return;
    setBusyAction(`import:${mode}`); setFeedback("");
    try {
      const result = await backend<ResearchImportResult>("ResearchFacade", "ImportCandidate", { projectId: project.id, candidateId: selected.id, mode, researchTaskId: scopedTaskId || undefined });
      setPage((current) => ({ ...current, items: current.items.map((item) => item.id === result.candidate.id ? result.candidate : item) }));
      setFeedback(result.candidate.importKind === "full_text" ? "开放全文已保存为研究材料，后台索引正在建立；不会自动加入资料库。" : "题录/摘要已保存为研究材料（非全文），后台索引正在建立；不会自动加入资料库。");
    } catch (error) {
      setFeedback(errorText(error));
      await loadCandidates(page.offset).catch(() => undefined);
    } finally { setBusyAction(""); }
  }

  function toggleSource(sourceId: string) {
    setSelectedSources((current) => current.includes(sourceId) ? current.filter((item) => item !== sourceId) : [...current, sourceId]);
  }

  async function deleteCandidates() {
    if (!checkedCandidates.length || discoveryBusy || !effectiveQueryId) return;
    setBusyAction("delete-candidates");
    try {
      if (!await appConfirm({title:`移除 ${checkedCandidates.length} 篇候选？`,message:"仅从当前检索记录移除。其他检索、已导入材料和科研产物不受影响。",confirmLabel:"移除候选",tone:"danger"})) return;
      await backend<void>("ResearchFacade","DeleteQueryCandidates",{projectId:project.id,queryId:effectiveQueryId,candidateIds:checkedCandidates});
      setCheckedCandidates([]);await loadCandidates(0);setHistoryRevision((value) => value + 1);
    } catch(error) {setFeedback(errorText(error));} finally {setBusyAction("");}
  }

  return <ModalBackdrop className="modal-backdrop research-discovery-backdrop" close={close} busy={Boolean(busyAction)}><section className="research-modal" role="dialog" aria-modal="true" aria-label="文献发现">
    <header><div><span className="dialog-icon research"><Icon name="search" size={19}/></span><div><p>LITERATURE DISCOVERY</p><h2>{project.name} · 文献发现</h2></div></div><button type="button" className="close" aria-label="关闭文献发现" data-dialog-dismiss onClick={close}><Icon name="close"/></button></header>
    <form className="research-searchbar" onSubmit={runSearch}><div><Icon name="search" size={16}/><input value={queryText} onChange={(event) => setQueryText(event.target.value)} placeholder="输入题名、作者、DOI、主题词或布尔检索式" maxLength={500}/></div><button type="submit" disabled={discoveryBusy || !queryText.trim() || selectedSources.length === 0}><Icon name={searching ? "refresh" : "search"} size={15}/>{searching ? "正在检索" : "检索并保存"}</button></form>
    <div className="research-source-picker">{sources.map((source) => <button type="button" className={selectedSources.includes(source.id) ? "selected" : ""} onClick={() => toggleSource(source.id)} key={source.id} title={source.description}><span className="research-source-check">{selectedSources.includes(source.id) && <Icon name="check" size={11}/>}</span><b>{source.name}</b><small>{source.fullText ? "开放全文" : "元数据"}</small></button>)}</div>
    {scopedTaskId && <div className="discovery-material-toolbar"><span>检索与筛选留在本任务，主动加入的资料才会出现在资料库。</span><button type="button" onClick={()=>setMaterialView({taskId:scopedTaskId,attachmentId:""})}>管理本任务材料</button></div>}
    {materialView && createPortal(<ResearchMaterialsLibrary key={`${project.id}:${materialView.taskId}:${materialView.attachmentId}`} project={project} taskId={materialView.taskId} taskMaterials initialAttachmentId={materialView.attachmentId} service={backend} close={()=>{setMaterialView(null);setMaterialRefresh(value=>value+1);}}/>, document.body)}
    <details className="research-discovery-details"><summary>检索详情</summary><div className="research-query-list">{visibleQueries.map((item) => <button type="button" key={item.id} onClick={() => selectQuery(item.id)} aria-current={effectiveQueryId === item.id ? "true" : undefined}>{item.text}</button>)}<div><button type="button" disabled={historyOffset===0 || discoveryBusy} onClick={()=>setHistoryOffset(Math.max(0,historyOffset-50))}>上一页</button><button type="button" disabled={discoveryBusy || historyOffset+50 >= (originEntries.find(e=>e.id===originFilter)?.count||0)} onClick={()=>setHistoryOffset(historyOffset+50)}>下一页</button></div></div>{activeQuery && <div className="research-source-status">{activeQuery.sources.map(item=><div key={item.sourceId}><strong>{item.sourceId}</strong><span>{item.message || `${item.count} 条`}</span></div>)}</div>}</details>
    {evidenceCandidate ? <BibliographyEvidenceWorkspace project={project} candidate={evidenceCandidate} taskId={scopedTaskId} sourceNames={Object.fromEntries(sources.map((item) => [item.id, item.name]))} close={() => setEvidenceCandidateId("")} feedback={setFeedback}/> : <div className="research-layout">
      <aside className="research-query-panel">
        <div className="research-panel-heading"><b title={project.name}>{project.name} · 任务</b></div>
        <nav className="research-origin-nav" aria-label="文献任务来源">
          {originEntries.map((entry) => <button type="button" key={entry.id} title={entry.title} aria-current={originFilter === entry.id ? "true" : undefined} className={originFilter === entry.id ? "selected" : ""} disabled={discoveryBusy} onClick={() => selectOrigin(entry.id)}>
            <span className="research-origin-icon"><Icon name={entry.id === "all" ? "library" : entry.id === "manual" ? "search" : "folder"} size={16}/></span>
            <span className="research-origin-name">{entry.createdAt && <time>{new Date(entry.createdAt).toLocaleString("zh-CN", {year:"numeric",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit"})}</time>}{entry.title}</span>
          </button>)}
        </nav>
        <button type="button" className="research-history-clear" disabled={!visibleQueries.length || discoveryBusy} onClick={() => void deleteHistory([], true)} title="删除当前任务的文献发现"><Icon name="trash" size={15}/>清空当前分组</button>
      </aside>
      <section className="research-results-panel"><div className="research-results-toolbar"><div><Icon name="search" size={14}/><input value={filterText} onChange={(event) => setFilterText(event.target.value)} placeholder="筛选当前候选"/></div><select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value as "" | ResearchReviewStatus)}><option value="">全部状态</option><option value="pending">待筛选</option><option value="included">{scopedTaskId ? "已纳入本任务" : "已纳入研究范围"}</option><option value="excluded">已排除</option></select><select value={sort} onChange={(event) => setSort(event.target.value)}><option value="relevance">相关度</option><option value="year_desc">年份从新到旧</option><option value="cited_desc">被引量</option><option value="title">题名字母序</option><option value="updated">最近更新</option></select></div><div className="research-result-count"><label><input type="checkbox" aria-label="全选本页文献候选" checked={page.items.length > 0 && page.items.every((item) => checkedCandidates.includes(item.id))} disabled={discoveryBusy || !page.items.length} onChange={(event) => setCheckedCandidates(event.target.checked ? page.items.map((item) => item.id) : [])}/>{page.total} 个候选 · 当前显示 {page.items.length}</label><button type="button" title="从本次检索移除所选候选" aria-label="移除所选文献候选" disabled={discoveryBusy || !checkedCandidates.length} onClick={() => void deleteCandidates()}><Icon name="trash" size={14}/></button></div><div className="research-candidate-list">{page.items.length === 0 ? <div className="research-empty"><Icon name="search" size={25}/><b>{queryId ? "当前筛选没有候选" : "先执行或选择一个检索"}</b><p>来源失败与真正无结果会分别显示，不会混为零结果。</p></div> : page.items.map((item) => <div className="research-candidate-row" key={item.id}><input type="checkbox" aria-label={`选择文献候选：${item.preferred.title}`} checked={checkedCandidates.includes(item.id)} disabled={discoveryBusy} onChange={(event) => setCheckedCandidates((current) => event.target.checked ? [...current,item.id] : current.filter((id) => id !== item.id))}/><button type="button" className={`${candidateId === item.id ? "selected" : ""} ${item.reviewStatus}`} key={item.id} onClick={() => setCandidateId(item.id)}><div><span className={`research-review-dot ${item.reviewStatus}`}/><b>{item.preferred.title}</b></div><p>{researchAuthorLine(item.preferred)}</p><footer><span>{item.preferred.year || "年份缺失"}{item.preferred.venue ? ` · ${item.preferred.venue}` : ""}</span><span>{item.records.length} 个来源</span><i className={item.importStatus}>{researchImportText[item.importStatus]}</i></footer></button></div>)}</div>{page.total > page.limit && <div className="research-pagination"><span>{page.offset + 1}-{Math.min(page.offset + page.items.length, page.total)} / {page.total}</span><button type="button" disabled={discoveryBusy || page.offset === 0} onClick={() => void loadCandidates(Math.max(0, page.offset - page.limit))}>上一页</button><button type="button" disabled={discoveryBusy || page.offset + page.limit >= page.total} onClick={() => void loadCandidates(page.offset + page.limit)}>下一页</button></div>}</section>
      <main className="research-detail-panel">{!selected ? <div className="research-empty detail"><Icon name="library" size={26}/><b>选择一个候选</b><p>核对聚合来源、摘要和标识符后决定纳入或排除。</p></div> : <>{activeQuery?.taskDeleted && <p role="status">原任务已删除，此检索记录只读。</p>}{activeQuery?.legacySnapshot && <p role="status">旧记录未保存独立快照，内容为现存来源信息；任务记录仅供查阅。</p>}<section className="research-detail-title"><div><span className={`research-review-dot ${selected.reviewStatus}`}/><div><h3>{selected.preferred.title}</h3><p>{researchAuthorLine(selected.preferred)}</p></div></div><span className={selected.reviewStatus}>{selected.reviewStatus === "included" ? scopedTaskId ? "已纳入本任务" : "已纳入研究范围" : researchReviewText[selected.reviewStatus]}</span></section><button type="button" className="research-bibliography-open" disabled={historyReadOnly} onClick={() => setEvidenceCandidateId(selected.id)}><Icon name="library" size={14}/><span><b>规范书目与证据矩阵</b><small>核对字段来源、修订书目并定位本地证据</small></span><Icon name="back" size={13}/></button><div className="research-work-meta"><span>{selected.preferred.year || "年份缺失"}</span>{selected.preferred.venue && <span>{selected.preferred.venue}</span>}{selected.preferred.citedByCount !== undefined && <span>被引 {selected.preferred.citedByCount}</span>}{selected.preferred.openAccess && <span className="open">有开放全文线索</span>}</div>{researchIdentifiers(selected.preferred).length > 0 && <div className="research-identifier-list">{researchIdentifiers(selected.preferred).map((value) => <code key={value}>{value}</code>)}</div>}<section className="research-abstract"><b>来源摘要</b><p>{selected.preferred.abstract || "当前来源没有提供摘要。"}</p></section><details className="research-records"><summary>{selected.records.length} 条来源记录与字段冲突</summary>{selected.records.map((record) => <article key={record.id}><header><b>{sources.find((source) => source.id === record.work.sourceId)?.name ?? record.work.sourceId}</b><code>{record.work.sourceRecordId}</code></header><p>{record.work.title}</p><small>{[record.work.year, record.work.venue, record.work.identifiers?.doi, record.work.identifiers?.pmid].filter(Boolean).join(" · ") || "无补充书目信息"}</small></article>)}</details><section className="research-review-editor"><label>筛选笔记<textarea disabled={historyReadOnly} value={note} onChange={(event) => setNote(event.target.value)} maxLength={20000} placeholder="记录纳入标准、质量判断或后续核对事项"/></label><label className={selected.reviewStatus === "excluded" ? "required" : ""}>排除原因<textarea disabled={historyReadOnly} value={exclusionReason} onChange={(event) => setExclusionReason(event.target.value)} maxLength={2000} placeholder="排除时必填，例如：研究对象不符合范围"/></label><div><button type="button" disabled={discoveryBusy || historyReadOnly} onClick={() => void updateReview("pending")}>待定</button><button type="button" className="exclude" disabled={discoveryBusy || historyReadOnly} onClick={() => void updateReview("excluded")}><Icon name="close" size={13}/>排除</button><button type="button" className="include" disabled={discoveryBusy || historyReadOnly} onClick={() => void updateReview("included")}><Icon name="check" size={13}/>{scopedTaskId ? "纳入本任务" : "纳入研究范围"}</button><button type="button" disabled={discoveryBusy || historyReadOnly} onClick={() => void saveNote()}>保存笔记</button></div></section><section className="discovery-library-action"><div><b>保存为自己的参考资料</b><small>待定、纳入或排除均可保存，不改变本任务筛选结论。</small></div><button type="button" disabled={discoveryBusy || Boolean(activeQuery?.taskDeleted)} onClick={()=>void collectCandidate()}>{busyAction === "collect" ? "正在加入…" : "加入资料库"}</button><button type="button" onClick={()=>openKnowledge()}>打开资料库</button></section><section className={`research-import-panel ${selected.importStatus}`}><header><div><b>{scopedTaskId ? "保存本任务材料" : "保存研究材料"}</b><small>{selected.importStatus === "imported" ? (selected.importKind === "full_text" ? "已保存开放全文" : "已保存题录/摘要，非全文") : researchImportText[selected.importStatus]}</small></div>{selectedMaterial && <button type="button" onClick={() => setMaterialView({taskId:scopedTaskId,attachmentId:selectedMaterial.id})}><Icon name="library" size={13}/>预览已保存材料</button>}</header>{selectedMaterial && <p>索引片段 {selectedMaterial.indexChunks ?? "待准备"} 个 · 提取 {(selectedMaterial.extractedRunes ?? 0).toLocaleString()} 字 · {selectedMaterial.parseSummary || "可预览保存的材料"}</p>}{selected.importError && <p className={selected.importStatus === "imported" ? "" : "error"}>{selected.importStatus === "imported" ? "材料提示：" : ""}{selected.importError}</p>}{selected.importStatus !== "imported" && <div><button type="button" disabled={selected.reviewStatus !== "included" || discoveryBusy || historyReadOnly} onClick={() => void importCandidate("auto")}><Icon name="download" size={13}/>自动保存</button><button type="button" disabled={selected.reviewStatus !== "included" || discoveryBusy || historyReadOnly} onClick={() => void importCandidate("full_text")}>保存开放全文</button><button type="button" disabled={selected.reviewStatus !== "included" || discoveryBusy || historyReadOnly} onClick={() => void importCandidate("metadata_abstract")}>保存题录/摘要</button></div>}{selected.importStatus === "imported" && selected.importKind !== "full_text" && <button type="button" disabled={selected.reviewStatus !== "included" || discoveryBusy || historyReadOnly} onClick={() => void importCandidate("full_text")}><Icon name="download" size={13}/>获取开放全文</button>}<p>{scopedTaskId ? "保存在本项目的当前任务中，供后台索引和研究分析使用，不会自动加入资料库。" : "保存在本项目的研究材料中，供后台索引使用，不会自动加入资料库。自动保存优先摘要，缺摘要时尝试开放全文，不可获取时保留题录并注明限制。"}</p></section></>}</main>
    </div>}{feedback && <div className="research-feedback"><Icon name="check" size={14}/><span>{feedback}</span><button type="button" onClick={() => setFeedback("")}><Icon name="close" size={13}/></button></div>}
  </section></ModalBackdrop>;
}

function ArtifactDocumentPreview({ document, citations }: { document: ArtifactStructuredPreview; citations: unknown }) {
  return <CitedDocument citations={citations}>{(map, select) => {
    const inline = (text?: string) => <CitationText text={text} citations={map} selectReference={select}/>;
    return <div className="artifact-structured-preview">
    {document.blocks.map((block, index) => {
      if (block.kind === "heading") {
        const level = Math.min(3, Math.max(1, block.level ?? 1));
        const Heading = `h${level + 2}` as "h3" | "h4" | "h5";
        return <Heading key={index}>{inline(block.text)}</Heading>;
      }
      if (block.kind === "paragraph") return <p key={index}>{inline(block.text)}</p>;
      if (block.kind === "list_item") return <div className="artifact-preview-list" key={index}><span>•</span><p>{inline(block.text)}</p></div>;
      if (block.kind === "quote") return <blockquote key={index}>{inline(block.text)}</blockquote>;
      if (block.kind === "code") return <pre key={index}>{block.text}</pre>;
      if (block.kind === "rule") return <hr key={index}/>;
      if (block.kind === "table") return <div className="artifact-preview-table-wrap" key={index}><table><tbody>{(block.rows ?? []).map((row, rowIndex) => <tr key={rowIndex}>{row.map((cell, columnIndex) => rowIndex === 0 ? <th key={columnIndex}>{inline(cell)}</th> : <td key={columnIndex}>{inline(cell)}</td>)}</tr>)}</tbody></table></div>;
      return null;
    })}
  </div>;
  }}</CitedDocument>;
}

function ArtifactLibrary({ project, taskId = "", close }: { project: Project; taskId?: string; close: () => void }) {
  const [artifacts, setArtifacts] = useState<ResearchArtifact[]>([]);
  const [researchTasks, setResearchTasks] = useState<ResearchTask[]>([]);
  const [selectedScopeKey, setSelectedScopeKey] = useState(() => taskId.trim() ? `task:${taskId.trim()}` : "");
  const [includeTrashed, setIncludeTrashed] = useState(false);
  const [selectedId, setSelectedId] = useState("");
  const [detail, setDetail] = useState<ArtifactDetail | null>(null);
  const [detailRevision, setDetailRevision] = useState(0);
  const [versionId, setVersionId] = useState("");
  const [preview, setPreview] = useState<ArtifactPreview | null>(null);
  const [integrity, setIntegrity] = useState<ArtifactIntegrity | null>(null);
  const [loading, setLoading] = useState(true);
  const [busyAction, setBusyAction] = useState("");
  const [selectedArtifactIds, setSelectedArtifactIds] = useState<string[]>([]);
  const [feedback, setFeedback] = useState("");
  const [exportFormat, setExportFormat] = useState<ArtifactExportFormat>("docx");
  const [citationStyle, setCitationStyle] = useState<ArtifactCitationStyle>("gb_t_7714_2015");

  useEffect(() => {
    let active = true;
    void backend<ResearchTask[]>("WorkflowFacade", "ListResearchTasks", project.id, 500)
      .then((values) => { if (active) setResearchTasks(values ?? []); })
      .catch(() => { /* Resource labels are best effort; IDs remain a safe fallback. */ });
    return () => { active = false; };
  }, [project.id]);

  const load = useCallback(async (preferred = "") => {
    setLoading(true);
    try {
      const values = await backend<ResearchArtifact[]>("ArtifactFacade", taskId.trim() ? "ListTaskArtifacts" : "ListArtifacts", ...(taskId.trim() ? [project.id, taskId.trim(), includeTrashed] : [project.id, includeTrashed]));
      setArtifacts(values);
      setSelectedId((current) => values.some((item) => item.id === (preferred || current)) ? (preferred || current) : "");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setLoading(false); }
  }, [includeTrashed, project.id, taskId]);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    setDetail(null); setPreview(null); setIntegrity(null);
    if (!selectedId) { setDetail(null); setVersionId(""); return; }
    let active = true;
    void backend<ArtifactDetail>("ArtifactFacade", taskId.trim() ? "GetTaskArtifact" : "GetArtifact", ...(taskId.trim() ? [project.id, taskId.trim(), selectedId] : [project.id, selectedId])).then((value) => {
      if (!active) return;
      setDetail(value);
      setVersionId((current) => value.versions.some((item) => item.id === current) ? current : value.artifact.currentVersionId || first(value.versions)?.id || "");
    }).catch((error: unknown) => active && setFeedback(errorText(error)));
    return () => { active = false; };
  }, [detailRevision, project.id, selectedId, taskId]);
  useEffect(() => {
    setIntegrity(null); setPreview(null);
    if (!versionId) return;
    let active = true;
    void backend<ArtifactPreview>("ArtifactFacade", taskId.trim() ? "PreviewTaskArtifactVersion" : "PreviewArtifactVersion", ...(taskId.trim() ? [project.id, taskId.trim(), versionId] : [project.id, versionId])).then((value) => active && setPreview(value)).catch((error: unknown) => {
      if (active) setPreview({ versionId, kind: "binary", mimeType: "application/octet-stream", truncated: false });
      if (active && !String(errorText(error)).includes("inline preview")) setFeedback(errorText(error));
    });
    return () => { active = false; };
  }, [project.id, taskId, versionId]);

  const selected = detail?.artifact.id === selectedId ? detail.artifact : undefined;
  const version = detail?.artifact.id === selectedId ? detail.versions.find((item) => item.id === versionId) ?? first(detail.versions) : undefined;
  const selectedWritable = !taskId.trim() || selected?.scopeKind === "task" && selected.researchTaskId === taskId.trim();
  const activeCount = artifacts.filter((item) => item.status === "active").length;
  const trashedCount = artifacts.filter((item) => item.status === "trashed").length;
  const scopeEntries = buildResourceScopes(artifacts, researchTasks, taskId);
  const scopedArtifacts = artifacts.filter((item) => resourceScopeKey(item) === selectedScopeKey);
  const scopeWritable = !taskId.trim() || selectedScopeKey === `task:${taskId.trim()}`;
  const selectableArtifacts = scopedArtifacts.filter((item) => item.status === "active" && scopeWritable);
  const artifactNode = (item: ResearchArtifact) => {
    const selectable = item.status === "active" && scopeWritable;
    return <div className={`resource-tree-selectable ${selectedArtifactIds.includes(item.id) ? "checked" : ""}`} key={item.id}>
      {selectable && <button type="button" className="resource-select-toggle" aria-label={`选择 ${item.name}`} aria-pressed={selectedArtifactIds.includes(item.id)} onClick={() => setSelectedArtifactIds((current) => current.includes(item.id) ? current.filter((id) => id !== item.id) : [...current, item.id])}><Icon name={selectedArtifactIds.includes(item.id) ? "check" : "plus"} size={11}/></button>}
      <button type="button" className={`resource-tree-node ${selectedId === item.id ? "selected" : ""} ${item.status}`} onClick={() => { setFeedback(""); setSelectedId(item.id); }} title={`打开 ${item.name} 详情`}>
        <span className="artifact-kind"><Icon name={item.kind === "image" ? "model" : item.kind === "data" ? "chart" : item.kind === "code" ? "tool" : "archive"} size={15}/></span>
        <span><b title={item.name}>{item.name}</b><small>{artifactKindText[item.kind]} · {artifactOriginLabel(item)} · {item.currentVersion ? `v${item.currentVersion.versionNumber}` : "无版本"}</small></span>
        {item.status === "trashed" && <i>回收站</i>}
      </button>
    </div>;
  };
  const sourceArtifacts = scopedArtifacts.filter((item) => artifactTreeStage(item) === "source");
  const processArtifacts = scopedArtifacts.filter((item) => artifactTreeStage(item) === "process");
  const resultArtifacts = scopedArtifacts.filter((item) => artifactTreeStage(item) === "result");
  const exportNodes = detail?.artifact.id === selectedId ? detail.versions.flatMap((item) => item.exports.map((exported) => <button type="button" className={`resource-tree-node export ${versionId === item.id ? "selected" : ""}`} key={exported.id} onClick={() => { setVersionId(item.id); }}>
    <span className="artifact-kind"><Icon name="download" size={15}/></span><span><b title={exported.fileName}>{exported.fileName}</b><small>{exported.format.toUpperCase()} · 来源 v{item.versionNumber} · {fileSize(exported.sizeBytes)}</small></span>
  </button>)) : [];
  const artifactColumns: ResourceTreeColumn[] = [
    { key: "source", title: "输入 / 来源", subtitle: "原始数据与手动登记", items: sourceArtifacts.map(artifactNode) },
    { key: "process", title: "分析与过程", subtitle: "代码、预检与过程记录", items: processArtifacts.map(artifactNode) },
    { key: "result", title: "研究成果", subtitle: "报告、图表与结论", items: resultArtifacts.map(artifactNode) },
    { key: "export", title: "正式导出", subtitle: "选中成果的发布版本", items: exportNodes },
  ];

  useEffect(() => {
    if (!scopeEntries.some((entry) => entry.key === selectedScopeKey)) {
      setSelectedScopeKey(scopeEntries.find((entry) => entry.count > 0)?.key ?? scopeEntries[0]?.key ?? "");
      return;
    }
    if (!scopedArtifacts.some((item) => item.id === selectedId)) setSelectedId("");
  }, [artifacts, researchTasks, selectedId, selectedScopeKey, taskId]);
  useEffect(() => {
    const selectableIds = new Set(selectableArtifacts.map((item) => item.id));
    setSelectedArtifactIds((current) => current.filter((id) => selectableIds.has(id)));
  }, [artifacts, selectedScopeKey, taskId]);

  async function registerFile(artifactId = "") {
    if (busyAction) return;
    setBusyAction(artifactId ? "version" : "add"); setFeedback("");
    try {
      const result = await backend<ArtifactSaveResult>("ArtifactFacade", taskId.trim() ? "ChooseAndRegisterTaskWorkspaceFile" : "ChooseAndRegisterWorkspaceFile", ...(taskId.trim() ? [project.id, taskId.trim(), artifactId] : [project.id, artifactId]));
      if (!result.artifact.id) return;
      setSelectedScopeKey(resourceScopeKey(result.artifact));
      await load(result.artifact.id);
      setDetailRevision((value) => value + 1);
      setFeedback(result.created ? `已登记科研产物：${result.artifact.name}` : `已保存 v${result.version.versionNumber}：${result.artifact.name}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function renameArtifact() {
    if (!selected || busyAction) return;
    const name = (await appPrompt({ title: "重命名科研产物", message: "名称只影响产物库中的显示，不会改变任何历史版本内容。", initialValue: selected.name, placeholder: "输入科研产物名称", confirmLabel: "保存名称" }))?.trim();
    if (!name || name === selected.name) return;
    setBusyAction("rename");
    try { await backend("ArtifactFacade", taskId.trim() ? "RenameTaskArtifact" : "RenameArtifact", ...(taskId.trim() ? [project.id, taskId.trim(), selected.id, name] : [project.id, selected.id, name])); await load(selected.id); setDetailRevision((value) => value + 1); setFeedback("产物名称已更新，历史版本内容未改变。"); }
    catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function changeStatus() {
    if (!selected || busyAction) return;
    const trashing = selected.status === "active";
    if (trashing && !await appConfirm({ title: `将“${selected.name}”移入回收站？`, message: "所有不可变版本仍会保留，可随时恢复。", confirmLabel: "移入回收站", tone: "danger" })) return;
    setBusyAction("status");
    try {
      const method = taskId.trim() ? (trashing ? "TrashTaskArtifact" : "RestoreTaskArtifact") : (trashing ? "TrashArtifact" : "RestoreArtifact");
      await backend("ArtifactFacade", method, ...(taskId.trim() ? [project.id, taskId.trim(), selected.id] : [project.id, selected.id]));
      await load(selected.id);
      setDetailRevision((value) => value + 1);
      if (trashing) setSelectedId("");
      setFeedback(trashing ? "已移入回收站。" : "科研产物已恢复。");
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function trashSelectedArtifacts() {
    const selectedValues = selectableArtifacts.filter((item) => selectedArtifactIds.includes(item.id));
    if (!selectedValues.length || busyAction || !await appConfirm({ title: `将 ${selectedValues.length} 个科研产物移入回收站？`, message: "所有不可变版本仍会保留，可在显示回收站后恢复。", confirmLabel: "批量移入回收站", tone: "danger" })) return;
    setBusyAction("batch-trash"); setFeedback("");
    let succeeded = 0;
    const errors: string[] = [];
    const failedIds = new Set<string>();
    const method = taskId.trim() ? "TrashTaskArtifact" : "TrashArtifact";
    for (const item of selectedValues) {
      try {
        await backend("ArtifactFacade", method, ...(taskId.trim() ? [project.id, taskId.trim(), item.id] : [project.id, item.id]));
        succeeded += 1;
      } catch (error) { failedIds.add(item.id); errors.push(`${item.name}：${errorText(error)}`); }
    }
    setSelectedArtifactIds([]);
    if (selectedValues.some((item) => item.id === selectedId && !failedIds.has(item.id))) setSelectedId("");
    await load();
    setFeedback(errors.length ? `已移入回收站 ${succeeded} 个，失败 ${errors.length} 个：${errors[0]}` : `已将 ${succeeded} 个科研产物移入回收站。`);
    setBusyAction("");
  }

  async function checkIntegrity() {
    if (!version || busyAction) return;
    setBusyAction("integrity");
    try { setIntegrity(await backend<ArtifactIntegrity>("ArtifactFacade", taskId.trim() ? "CheckTaskArtifactIntegrity" : "CheckArtifactIntegrity", ...(taskId.trim() ? [project.id, taskId.trim(), version.id] : [project.id, version.id]))); }
    catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function downloadVersion() {
    if (!version || busyAction) return;
    setBusyAction("download");
    try {
      const path = await backend<string>("ArtifactFacade", taskId.trim() ? "DownloadTaskArtifactVersion" : "DownloadArtifactVersion", ...(taskId.trim() ? [project.id, taskId.trim(), version.id, version.fileName] : [project.id, version.id, version.fileName]));
      if (path) setFeedback(`已下载到：${path}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function createExport() {
    if (!version || busyAction) return;
    setBusyAction("export"); setFeedback("");
    try {
      const request = { projectId: project.id, versionId: version.id, format: exportFormat, citationStyle };
      const result = await backend<ArtifactExportResult>("ArtifactFacade", taskId.trim() ? "CreateTaskArtifactExport" : "CreateArtifactExport", ...(taskId.trim() ? [taskId.trim(), request] : [request]));
      setDetailRevision((value) => value + 1);
      setFeedback(result.created ? `已生成 ${result.export.format.toUpperCase()} 导出：${result.export.fileName}` : `相同来源和选项已存在，已复用：${result.export.fileName}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  async function downloadExport(value: ArtifactExport) {
    if (busyAction) return;
    setBusyAction(`export-download:${value.id}`);
    try {
      const path = await backend<string>("ArtifactFacade", taskId.trim() ? "DownloadTaskArtifactExport" : "DownloadArtifactExport", ...(taskId.trim() ? [project.id, taskId.trim(), value.id, value.fileName] : [project.id, value.id, value.fileName]));
      if (path) setFeedback(`已下载到：${path}`);
    } catch (error) { setFeedback(errorText(error)); }
    finally { setBusyAction(""); }
  }

  return <ModalBackdrop className="modal-backdrop" close={close} busy={Boolean(busyAction)}><section className="model-modal artifact-modal" role="dialog" aria-modal="true">
    <header><div><span className="dialog-icon"><Icon name="archive" size={19}/></span><div><p>RESEARCH ARTIFACTS</p><h2>{project.name} · 科研产物</h2></div></div><div className="artifact-header-actions"><button type="button" className="artifact-add" disabled={Boolean(busyAction)} onClick={() => void registerFile()}><Icon name={busyAction === "add" ? "refresh" : "plus"} size={15}/>{busyAction === "add" ? "正在登记" : "登记 Workspace 文件"}</button><button type="button" className="close" data-dialog-dismiss onClick={close}><Icon name="close"/></button></div></header>
    <div className="artifact-layout resource-browser-layout">
      <ResourceScopeNav entries={scopeEntries} selectedKey={selectedScopeKey} onSelect={setSelectedScopeKey}/>
      <div className="artifact-tree-wrap">
        <div className="artifact-list-summary"><span><b>{activeCount}</b> 个产物</span><label><input type="checkbox" checked={includeTrashed} onChange={(event) => setIncludeTrashed(event.target.checked)}/>显示回收站</label></div>
        {loading ? <div className="artifact-empty"><Icon name="refresh" size={22}/><span>正在读取科研产物</span></div> : <ResourceTreeFlow title={scopeEntries.find((entry) => entry.key === selectedScopeKey)?.title ?? "成果结构"} columns={artifactColumns} emptyText="暂无资源" actions={scopeWritable && selectableArtifacts.length ? <><button type="button" onClick={() => setSelectedArtifactIds(selectedArtifactIds.length === selectableArtifacts.length ? [] : selectableArtifacts.map((item) => item.id))}>{selectedArtifactIds.length === selectableArtifacts.length ? "取消全选" : "全选当前归属"}</button>{selectedArtifactIds.length > 0 && <button type="button" className="danger" disabled={Boolean(busyAction)} onClick={() => void trashSelectedArtifacts()}><Icon name={busyAction === "batch-trash" ? "refresh" : "trash"} size={13}/>移入回收站（{selectedArtifactIds.length}）</button>}</> : undefined}/>}
        {includeTrashed && trashedCount > 0 && <small className="artifact-trash-count">回收站中有 {trashedCount} 个产物</small>}
      </div>
      {selected && version && createPortal(<ModalBackdrop className="resource-detail-backdrop" close={() => setSelectedId("")} busy={Boolean(busyAction)}><section className="artifact-detail-dialog resource-detail-dialog" role="dialog" aria-modal="true" aria-label={`${selected.name} 详情`}>
        <header className="resource-detail-dialog-header"><div><span className="artifact-kind large"><Icon name={selected.kind === "image" ? "model" : selected.kind === "data" ? "chart" : selected.kind === "code" ? "tool" : "archive"} size={18}/></span><div><p>ARTIFACT DETAIL</p><h2>{selected.name}</h2></div></div><button type="button" className="close" aria-label="关闭产物详情" disabled={Boolean(busyAction)} data-dialog-dismiss onClick={() => setSelectedId("")}><Icon name="close"/></button></header>
        <main className="artifact-detail-panel">
        {feedback && <div className="artifact-detail-feedback" role="status"><Icon name="shield" size={14}/><span>{feedback}</span></div>}
        <section className="artifact-title"><div><span className="artifact-kind large"><Icon name={selected.kind === "image" ? "model" : selected.kind === "data" ? "chart" : selected.kind === "code" ? "tool" : "archive"} size={18}/></span><div><h3>{selected.name}</h3><p>{artifactKindText[selected.kind]} · {version.fileName} · {fileSize(version.sizeBytes)}</p></div></div>{selectedWritable && <div><button type="button" title="重命名" disabled={Boolean(busyAction)} onClick={() => void renameArtifact()}><Icon name="settings" size={14}/></button><button type="button" title={selected.status === "active" ? "移入回收站" : "恢复"} disabled={Boolean(busyAction)} onClick={() => void changeStatus()}><Icon name={selected.status === "active" ? "trash" : "refresh"} size={14}/></button></div>}</section>
        <div className="artifact-version-toolbar"><label>版本<select value={version.id} onChange={(event) => setVersionId(event.target.value)}>{detail?.versions.map((item) => <option value={item.id} key={item.id}>v{item.versionNumber} · {compactDate(item.createdAt)} · {artifactSourceText[item.sourceKind]}</option>)}</select></label><button type="button" disabled={Boolean(busyAction)} onClick={() => void checkIntegrity()}><Icon name={busyAction === "integrity" ? "refresh" : "shield"} size={14}/>完整性</button><button type="button" disabled={Boolean(busyAction)} onClick={() => void downloadVersion()}><Icon name={busyAction === "download" ? "refresh" : "download"} size={14}/>下载</button>{selectedWritable && selected.status === "active" && <button type="button" disabled={Boolean(busyAction)} title="从 Workspace 文件创建不可变新版本" onClick={() => void registerFile(selected.id)}><Icon name={busyAction === "version" ? "refresh" : "plus"} size={14}/>新版本</button>}</div>
        {integrity && <div className={`artifact-integrity ${integrity.status}`}><Icon name={integrity.status === "verified" ? "check" : "shield"} size={15}/><div><b>{integrity.status === "verified" ? "对象完整" : integrity.status === "missing" ? "对象缺失" : "校验不一致"}</b><small>{integrity.status === "verified" ? `SHA256 ${integrity.expectedSha256.slice(0, 16)}… · ${fileSize(integrity.actualSize)}` : integrity.message}</small></div></div>}
        <section className="artifact-export-panel"><header><div><b>正式文档导出</b><small>派生文件不会覆盖或推进原始版本</small></div>{version.exports.length > 0 && <span>{version.exports.length} 份历史导出</span>}</header>{artifactCanExport(version) ? selectedWritable ? <div className="artifact-export-controls"><label>格式<select value={exportFormat} onChange={(event) => setExportFormat(event.target.value as ArtifactExportFormat)}><option value="docx">DOCX</option><option value="pdf">PDF</option></select></label><label>引用样式<select value={citationStyle} onChange={(event) => setCitationStyle(event.target.value as ArtifactCitationStyle)}><option value="gb_t_7714_2015">GB/T 7714-2015</option><option value="apa_7">APA 7</option></select></label><button type="button" disabled={Boolean(busyAction)} onClick={() => void createExport()}><Icon name={busyAction === "export" ? "refresh" : "download"} size={14}/>{busyAction === "export" ? "正在生成" : "生成导出"}</button></div> : <div className="artifact-export-unavailable">项目共享产物在当前任务中只读，可下载已有版本和导出。</div> : <div className="artifact-export-unavailable">图片或不可提取的二进制文件不能转换为研究文档。</div>}{version.exports.length > 0 && <div className="artifact-export-list">{version.exports.map((item) => <div key={item.id}><span className="artifact-export-format">{item.format.toUpperCase()}</span><div><b title={item.fileName}>{item.fileName}</b><small>{artifactCitationStyleText[item.citationStyle]} · {fileSize(item.sizeBytes)} · {compactDate(item.createdAt)}</small></div><button type="button" title="下载此历史导出" aria-label={`下载 ${item.fileName}`} disabled={Boolean(busyAction)} onClick={() => void downloadExport(item)}><Icon name={busyAction === `export-download:${item.id}` ? "refresh" : "download"} size={14}/></button></div>)}</div>}</section>
        <section className="artifact-preview"><header><b>内容预览</b>{preview?.versionId === version.id && preview.truncated && <span>预览已按结构或长度截断</span>}</header>{!preview || preview.versionId !== version.id ? <div className="artifact-preview-loading">正在读取版本…</div> : preview.kind === "text" ? <CitedDocument key={version.id} citations={version.citations}>{(map, select) => <pre><CitationText text={preview.text} citations={map} selectReference={select}/></pre>}</CitedDocument> : preview.kind === "document" && preview.document ? <ArtifactDocumentPreview key={version.id} document={preview.document} citations={version.citations}/> : preview.kind === "image" ? <img alt={selected.name} src={`data:${preview.mimeType};base64,${preview.data}`}/> : <div className="artifact-binary"><Icon name="archive" size={26}/><b>此格式暂不提供内嵌预览</b><span>{version.mimeType} · {fileSize(version.sizeBytes)}</span></div>}</section>
        <div className="artifact-evidence-grid"><section><header><b>来源快照</b><span>{artifactSourceText[version.sourceKind]}</span></header><dl><div><dt>模型</dt><dd>{version.provenance.modelId || "非模型产物"}{version.provenance.modelProfileName ? ` · ${version.provenance.modelProfileName}` : ""}</dd></div><div><dt>来源</dt><dd>{version.provenance.toolName || version.provenance.conversationTitle || version.provenance.workspaceRelativePath || version.lineage[0]?.sourceIdSnapshot}</dd></div><div><dt>Skills</dt><dd>{version.provenance.skills?.length ? version.provenance.skills.map((item) => item.dynamic ? `${item.id} · 动态/${item.origin || "unknown"}` : `${item.id}@${item.version}`).join("、") : "本轮未加载 Skill"}</dd></div><div><dt>SHA256</dt><dd><code title={version.sha256}>{version.sha256.slice(0, 20)}…</code></dd></div></dl></section><section><header><b>可信引用</b><span>{version.citations.length} 条</span></header>{version.citations.length ? <div className="artifact-citations">{version.citations.map((item) => <div key={item.id}><b>{item.sourceName}</b><small>{item.locator || item.title || item.reference}</small><p>{item.quote}</p></div>)}</div> : <div className="artifact-no-citations">此版本没有采用结构化引用。</div>}</section></div>
        </main>
      </section></ModalBackdrop>, document.body)}
    </div>
    {feedback && <div className="artifact-feedback"><span>{feedback}</span><button type="button" onClick={() => setFeedback("")}><Icon name="close" size={13}/></button></div>}
  </section></ModalBackdrop>;
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
    if (kind === "rebuild" && !await appConfirm({ title: "重建项目 Python 环境？", message: "将按当前依赖锁创建新环境，并在验证成功后原子替换现有环境。", confirmLabel: "开始重建" })) return;
    if (kind === "bind" && !await appConfirm({ title: "绑定已有虚拟环境？", message: "SciAide 只登记和验证所选环境，不会接管、修改或删除其中的文件。", confirmLabel: "确认绑定" })) return;
    if (kind === "delete" && !await appConfirm({ title: environment?.environmentKind === "external" ? "解除外部环境绑定？" : "删除项目 Python 环境？", message: environment?.environmentKind === "external" ? "只会解除绑定，原虚拟环境目录和文件不会被删除。" : "项目环境的派生文件会删除；依赖锁声明仍会保留，可稍后重建。", confirmLabel: environment?.environmentKind === "external" ? "解除绑定" : "删除环境", tone: "danger" })) return;
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
  return <ModalBackdrop className="modal-backdrop" close={close} busy={Boolean(action)}><section className="python-environment-modal" role="dialog" aria-modal="true" aria-labelledby="python-environment-title">
    <header><div><span className="dialog-icon python"><Icon name="tool" size={19}/></span><div><p>PROJECT RUNTIME</p><h2 id="python-environment-title">{project.name} · Python 环境</h2></div></div><button type="button" className="close" data-dialog-dismiss onClick={close}><Icon name="close"/></button></header>
    {loading ? <div className="python-environment-loading"><Icon name="refresh" size={22}/><b>正在检测 Python 解释器和项目环境</b></div> : <div className="python-environment-body">
      <section className={`python-runtime-status ${environment?.state ?? "absent"}`}><span><Icon name={ready ? "check" : "tool"} size={20}/></span><div><small>环境状态 · {kindText}</small><b>{stateText}</b><p>{ready ? `${environment?.implementation} ${environment?.baseExecutableVersion} · ${environment?.architecture}` : environment?.errorMessage || "当前项目还没有独立 Python 虚拟环境。"}</p></div><code>{ready ? shortHash(environment?.environmentFingerprint) : "NO ENV"}</code></section>
      {!ready && !broken && <section className="python-environment-choices">
        <article><header><span><Icon name="tool" size={16}/></span><div><b>创建项目环境</b><small>推荐 · 使用系统 Python 创建隔离环境</small></div></header>{baseInterpreters.length ? <><label>创建来源<select value={basePath} disabled={Boolean(action)} onChange={(event) => setBasePath(event.target.value)}>{baseInterpreters.map((item) => <option key={item.executableSha256} value={item.executablePath}>{item.implementation} {item.version} · {item.executablePath}</option>)}</select></label><p>基础 Python 只负责创建。后续 Shell、Python 和 Kernel 使用 <code>{workspaceEnvironmentPath}\Scripts\python.exe</code>。</p></> : <div className="python-missing"><Icon name="shield" size={16}/><div><b>未检测到可创建环境的 Python 3</b><span>请先安装带 venv 的 64 位 Python 3，或手动选择基础 python.exe。</span></div></div>}<div className="python-choice-actions"><button type="button" disabled={Boolean(action)} onClick={() => void chooseBaseInterpreter()}><Icon name="folder" size={13}/>选择基础 Python</button><button type="button" className="primary" disabled={Boolean(action) || !selectedBase} onClick={() => void mutate("create")}><Icon name={action === "create" ? "refresh" : "tool"} size={14}/>创建项目环境</button></div></article>
        <article><header><span><Icon name="folder" size={16}/></span><div><b>使用已有虚拟环境</b><small>可选 · 直接作为项目工具运行环境</small></div></header><p>SciAide 只绑定和验证，不安装依赖、不删除文件。请选择 venv 或 conda 环境中的 python.exe。</p>{externalPath && <code className="python-external-path" title={externalPath}>{externalPath}</code>}<div className="python-choice-actions"><button type="button" disabled={Boolean(action)} onClick={() => void chooseExistingEnvironment()}><Icon name="folder" size={13}/>{externalPath ? "更换虚拟环境" : "选择已有虚拟环境"}</button><button type="button" className="primary" disabled={Boolean(action) || !externalPath} onClick={() => void mutate("bind")}><Icon name={action === "bind" ? "refresh" : "check"} size={13}/>确认绑定</button></div></article>
      </section>}
      {ready && <section className="python-environment-facts"><div><span>实际项目运行解释器</span><code title={environment.environmentPythonPath}>{environment.environmentPythonPath}</code></div><div><span>环境类型</span><b>{kindText}</b><small>{external ? "用户维护文件" : "SciAide 托管派生文件"}</small></div><div><span>依赖锁</span><b>{environment.lock.length} 个包</b><small title={environment.freezeSha256}>{shortHash(environment.freezeSha256)}</small></div><div><span>{external ? "环境路径" : "创建来源（仅创建时使用）"}</span><code title={environment.baseExecutablePath}>{environment.baseExecutablePath}</code></div></section>}
      <section className="python-actions">{!external && (ready || broken) && <button type="button" className="primary" disabled={Boolean(action) || !basePath} onClick={() => void mutate("rebuild")}><Icon name={action === "rebuild" ? "refresh" : "tool"} size={14}/>{managedActionText}</button>}<button type="button" disabled={Boolean(action) || !ready && !broken} onClick={() => void mutate("verify")}><Icon name={action === "verify" ? "refresh" : "check"} size={14}/>验证运行环境</button><button type="button" disabled={Boolean(action)} onClick={() => void mutate("stop")}><Icon name="stop" size={14}/>停止 Kernel</button><button type="button" className="danger" disabled={Boolean(action) || environment?.state === "absent"} onClick={() => void mutate("delete")}><Icon name="trash" size={14}/>{external ? "解除绑定" : "删除环境文件"}</button></section>
      <BrowserEnvironment service={backend} projectId={project.id} enabled={ready&&!external&&!legacy&&!action} onChanged={()=>{void backend<PythonEnvironment>("PythonFacade","GetProjectEnvironment",project.id).then(setEnvironment).catch(e=>setMessage(errorText(e)))}}/>
      {ready && <details className="python-lock"><summary>查看依赖锁 <span>{environment.lock.length}</span></summary><pre>{environment.lock.join("\n") || "环境仅包含 Python 标准库。"}</pre></details>}
      <div className="python-runtime-boundary"><Icon name="shield" size={16}/><div><b>Shell、一次性 Python 和项目 Kernel 默认可以联网</b><span>不会增加域名白名单或联网弹窗；Plan 仍确认整个高风险工具调用。依赖安装因修改项目环境而确认，API Key 和 MCP Secret 不会自动传入子进程。</span></div></div>
      {message && <div className="python-environment-message"><span>{message}</span><button type="button" onClick={() => setMessage("")}><Icon name="close" size={13}/></button></div>}
    </div>}
  </section></ModalBackdrop>;
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

function SkillSettings({ project }: { project?: Project }) {
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

  const savedUser=useRef({name:"my-research-skill",content:userSkillTemplate()});
  useDialogGuard({busy,dirty:userName!==savedUser.current.name||userContent!==savedUser.current.content});
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
        await appConfirm({
          title: "替换现有 Git Skill？",
          message: "该 namespace 已存在且内容不同。替换前版本会进入可恢复归档；已开始的 Run 仍使用原有快照。",
          confirmLabel: "确认替换",
          tone: "danger",
        })
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
      savedUser.current={name:"my-research-skill",content:userSkillTemplate()};
      setUserName("my-research-skill");
      setUserContent(userSkillTemplate());
      return;
    }
    setBusy(true);
    try {
      setUserName(item.name);
      const content=await backend<string>("SkillFacade", "ReadUserSkill", item.name);
      savedUser.current={name:item.name,content};setUserContent(content);
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
      savedUser.current={name:userName,content:userContent};
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
      !await appConfirm({
        title: `删除 User Skill “${item.name}”？`,
        message: "目录会移入可恢复归档；同名低优先级 Skill 会重新生效。",
        confirmLabel: "删除 Skill",
        tone: "danger",
      })
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
      !await appConfirm({
        title: `卸载 Git Skill “${item.name}”？`,
        message: "文件会移入可恢复归档；同名低优先级 Skill 会重新生效。",
        confirmLabel: "卸载 Skill",
        tone: "danger",
      })
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
    <SettingsPage title="Skills" description="管理研究技能、加载策略与扩展来源。" className="skills-settings-page">
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
      </SettingsPage>

  );
}

function MCPSettings() {
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [id, setId] = useState("");
  const [editing,setEditing]=useState(false);
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

  useDialogGuard({busy,dirty:Boolean(importJSON&&!importResult)||name!==(current?.name??"")||namespace!==(current?.namespace??"")||transport!==(current?.transport??"stdio")||command!==(current?.command??"")||args!==(current?.args?.join("\n")??"")||workingDir!==(current?.workingDir??"")||url!==(current?.url??"")||env!==(current&&Object.keys(current.env).length?JSON.stringify(current.env,null,2):"")||headers!==(current&&Object.keys(current.headers).length?JSON.stringify(current.headers,null,2):"")||Boolean(secretValues||clearSecrets.length)||trusted!==(current?.trust==="user_trusted")||enabled!==(current?.enabled??true)});
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
    if (!id || !await appConfirm({ title: "移除 MCP Server？", message: "Server 配置及系统凭据库中的关联 Secret 都会删除。", confirmLabel: "移除 Server", tone: "danger" })) return;
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

  return <SettingsPage title="MCP 服务" description="连接外部工具，管理服务权限与运行状态。" className="mcp-settings-page" actions={editing ? <button type="button" className="settings-back" onClick={()=>setEditing(false)}><Icon name="back" size={15}/>返回配置列表</button> : undefined}>
      {toast && <div className="mcp-toast" role="status"><span><Icon name="check" size={15}/></span><div><b>{toast.text}</b><small>{toast.detail}</small></div></div>}

      <div className={`settings-grid settings-collection ${editing?"is-editing":"is-list"}`}>
        <aside hidden={editing}>
          <button className={`add-profile ${!importOpen && !id ? "selected" : ""}`} onClick={() => { setImportOpen(false); setId(""); setEditing(true); }}><Icon name="plus"/> 添加 MCP Server</button>
          <button className={`add-profile import-profile ${importOpen ? "selected" : ""}`} onClick={() => { setImportOpen(true); setFeedback(""); setImportResult(null); setEditing(true); }}><Icon name="tool"/> 从 JSON 导入</button>
          {servers.length > 0 && <div className="mcp-batch-panel">
            <div><button type="button" className="mcp-select-all" onClick={toggleConnectable} disabled={busy || !connectable.length}><span className={`mcp-check ${allConnectableSelected ? "checked" : ""}`}>{allConnectableSelected && <Icon name="check" size={11}/>}</span>{allConnectableSelected ? "取消全选" : "全选可连接"}</button><small>已选 {selected.length}</small></div>
            <button type="button" className="mcp-connect-all" onClick={() => void batch("ConnectMCPServers", connectable.map((server) => server.id))} disabled={busy || !connectable.length}><Icon name="server" size={14}/> 一键连接全部 <span>{connectable.length}</span></button>
            <div className="mcp-selected-actions"><button type="button" onClick={() => void batch("ConnectMCPServers", selectedConnectable.map((server) => server.id))} disabled={busy || !selectedConnectable.length}>连接所选</button><button type="button" onClick={() => void batch("DisconnectMCPServers", selectedActive.map((server) => server.id))} disabled={busy || !selectedActive.length}>断开所选</button></div>
          </div>}
          {batchResult && <div className="mcp-batch-result"><b>最近批量操作</b><span>成功 {batchResult.succeeded} · 跳过 {batchResult.skipped} · 失败 {batchResult.failed}</span>{batchResult.items.filter((item) => item.status !== "succeeded").map((item) => <p className={item.status} key={`${item.serverId}-${item.status}`}><strong>{item.name || item.serverId || "未知 Server"}</strong><small>{item.message || item.status}</small></p>)}</div>}
          <div className="profile-caption">服务连接 · {servers.length}</div>{servers.length===0&&<p className="settings-empty-hint">添加服务或导入 JSON，连接你需要的外部工具。</p>}
          {servers.map((server) => <div className={`mcp-profile-row ${selectedIds.has(server.id) ? "checked" : ""}`} key={server.id}><button type="button" className="mcp-row-check" aria-label={`选择 ${server.name}`} aria-pressed={selectedIds.has(server.id)} onClick={() => toggleSelected(server.id)} disabled={busy}><span className={`mcp-check ${selectedIds.has(server.id) ? "checked" : ""}`}>{selectedIds.has(server.id) && <Icon name="check" size={11}/>}</span></button><button className={`profile-item ${!importOpen && server.id === id ? "selected" : ""}`} onClick={() => { setImportOpen(false); setId(server.id); setEditing(true); }} disabled={busy}>
            <span className="provider-logo"><Icon name="server" size={15}/></span>
            <span><b>{server.name}</b><small>{server.transport} · {server.toolCount} tools</small></span>
            <i className={`status-dot ${server.status === "ready" ? "ready" : server.status === "failed" ? "failed" : ""}`}/>
          </button></div>)}
        </aside>
        <form hidden={!editing} onSubmit={(event) => importOpen ? void importServers(event) : void save(event)}>
          {importOpen ? <>
          <section className="form-section mcp-import-section">
            <div className="form-heading"><span>JSON</span><div><h3>导入 MCP 配置</h3><p>兼容 Claude Desktop、Cursor、Codex 等常见的 mcpServers 结构</p></div></div>
            <div className="mcp-import-note"><Icon name="shield" size={17}/><div><b>导入不等于执行</b><span>配置会保存为“不受信任”且不会自动连接。请检查命令后，再手动确认信任并连接。</span></div></div>
            <label>mcpServers JSON<textarea className="mcp-import-editor" autoFocus spellCheck={false} value={importJSON} onChange={(event) => setImportJSON(event.target.value)} placeholder={'{\n  "mcpServers": {\n    "chrome-devtools": {\n      "command": "npx",\n      "args": ["-y", "chrome-devtools-mcp@latest"]\n    }\n  }\n}'} required disabled={busy}/><small>支持一次导入多个 Server；args 保持数组并直接传给进程，不经过 Shell 拼接。</small></label>
            <div className="mcp-import-security"><b>敏感信息如何保存？</b><p><code>env</code> 中名称包含 TOKEN、SECRET、PASSWORD、API_KEY、AUTH、CREDENTIAL 或 COOKIE 的值，会自动写入 Windows Credential Manager，不进入 SQLite。</p></div>
          </section>
          {importResult && <section className="mcp-import-result">
            {importResult.imported.length > 0 && <div className="imported"><b><Icon name="check" size={15}/> 已导入 {importResult.imported.length} 个 Server</b>{importResult.imported.map((server) => <button type="button" key={server.id} onClick={() => { setImportOpen(false); setId(server.id); setEditing(true); }}><span><strong>{server.name}</strong><small>{server.transport} · {server.namespace}</small></span><i>检查配置 →</i></button>)}</div>}
            {importResult.errors.length > 0 && <div className="import-errors"><b>有 {importResult.errors.length} 项未导入</b>{importResult.errors.map((error, index) => <p key={`${error.name}-${index}`}><strong>{error.name || "未命名 Server"}</strong><span>{error.message}</span></p>)}</div>}
          </section>}
          {feedback && <div className="feedback error">{feedback}</div>}
          <footer className="modal-actions mcp-import-actions"><span/><span/><button type="button" onClick={() => { setImportJSON(""); setImportResult(null); setEditing(true); }} disabled={busy || (!importJSON && !importResult)}>清空</button><button className="primary" disabled={busy || !importJSON.trim()}>{busy ? "导入中…" : "解析并导入"}</button></footer>
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
    </SettingsPage>
  ;
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

  return <ModalBackdrop className="modal-backdrop" close={close} ><section className="usage-modal" role="dialog" aria-modal="true" aria-labelledby="usage-title">
    <header><div><span className="dialog-icon gradient"><Icon name="chart"/></span><div><p>CLIENT-WIDE ANALYTICS</p><h2 id="usage-title">用量与缓存统计</h2></div></div><button className="close" data-dialog-dismiss onClick={close}><Icon name="close"/></button></header>
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
  </section></ModalBackdrop>;
}

function ModelSettings({
  profiles,
  refresh,
  select,
}: {
  profiles: Profile[];
  refresh: () => Promise<void>;
  select: (id: string) => void;
}) {
  const [id, setId] = useState("");
  const [editing,setEditing]=useState(false);
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
  useDialogGuard({busy:saving||discovering,dirty:name!==(current?.name??"")||apiProtocol!==(current?.apiProtocol??"openai_chat_completions")||baseUrl!==(current?.baseUrl??"https://api.openai.com/v1")||JSON.stringify(profileModels)!==JSON.stringify(current?.models??[])||Boolean(apiKey||manualModelId)||headers!==(current&&Object.keys(current.customHeaders??{}).length?JSON.stringify(current.customHeaders):"")});
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
      current && Object.keys(current.customHeaders ?? {}).length
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
      !await appConfirm({
        title: "删除模型与 API 配置？",
        message: "删除该配置及 API 密钥？历史聊天和科研记录会保留，但不能再使用该配置发起请求；仍在使用它的任务可能中断。",
        confirmLabel: "删除配置",
        tone: "danger",
      })
    )
      return;
    if(saving)return;setSaving(true);
    try {
      await backend<void>("ModelFacade", "DeleteModelProfile", id);
      setId("");
      await refresh();
      setFeedback({ kind: "ok", text: "模型配置已删除。" });
    } catch (error) {
      const detail = errorText(error);
        await appAlert({
          title: "删除模型配置失败",
          message: `SciAide 未能删除此配置。\n\n原因：${detail}`,
          confirmLabel: "知道了",
          tone: "danger",
        });
        setFeedback({ kind: "error", text: detail });
    } finally {setSaving(false);}
  }
  return (
    <SettingsPage title="模型与 API" description="管理模型连接、密钥与可用模型。" className="models-settings-page" actions={editing ? <button type="button" className="settings-back" onClick={()=>setEditing(false)}><Icon name="back" size={15}/>返回配置列表</button> : undefined}>
        {toast && <div className="mcp-toast" role="status"><span><Icon name="check" size={15}/></span><div><b>{toast.text}</b><small>{toast.detail}</small></div></div>}

        <div className={`settings-grid settings-collection ${editing?"is-editing":"is-list"}`}>
          <aside className="model-settings-sidebar" hidden={editing}>
            <button
              className={`add-profile ${!id && !visionFallbackOpen ? "selected" : ""}`}
              onClick={() => { setVisionFallbackOpen(false); setId(""); setEditing(true); }}
            >
              <Icon name="plus" /> 添加 API 配置
            </button>
            <div className="profile-caption">模型连接 · {profiles.length}</div>{profiles.length===0&&<p className="settings-empty-hint">还没有模型连接，添加 API 配置后即可使用。</p>}
            {profiles.map((profile) => (
              <button
                className={`profile-item ${profile.id === id && !visionFallbackOpen ? "selected" : ""}`}
                onClick={() => { setVisionFallbackOpen(false); setId(profile.id); setEditing(true); }}
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
            <div className="settings-service-entries">

            <button type="button" className={`vision-fallback-entry ${visionFallbackOpen ? "selected" : ""}`} onClick={() => { setVisionFallbackOpen(true); setEditing(true); }}>
              <span className="provider-logo"><Icon name="model" size={15}/></span>
              <span><b>识图兜底</b><small>配置自定义多模态模型</small></span>
              <Icon name="back" size={13}/>
            </button>
            </div>
          </aside>
          {visionFallbackOpen ? <div hidden={!editing} className="settings-vision"><VisionFallbackSettings/></div> : <form hidden={!editing} onSubmit={(event) => void save(event)}>
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
                      ? `已保存 ${current.secretMasked ?? "密钥"}`
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
      </SettingsPage>

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

type SearchChannel = { provider: string; enabled: boolean; priority: number; configured: boolean };
function WebSearchSettings({openNetwork}: {openNetwork:()=>void}) {
  const [dragging, setDragging] = useState("");
  const [dropTarget, setDropTarget] = useState("");
  const listRef = useRef<HTMLDivElement>(null);
  const [editing, setEditing] = useState("");
  const [confirmKeyDelete, setConfirmKeyDelete] = useState(false);
  const [testing, setTesting] = useState(false);
  const [channels, setChannels] = useState<SearchChannel[]>([]);
  const [keys, setKeys] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [query, setQuery] = useState("Python pandas read_csv documentation");
  const [result, setResult] = useState<{ status: string; provider: string; items: {title: string; url: string}[]; attempts: {provider: string; status: string}[] } | null>(null);
  useDialogGuard({busy});
  const labels: Record<string, string> = {baidu: "百度搜索", firecrawl: "Firecrawl", brave: "Brave Search", tavily: "Tavily", exa: "Exa", duckduckgo: "DuckDuckGo"};
  const keySources: Record<string, string> = {baidu:"https://console.bce.baidu.com/iam/#/iam/apikey/list",firecrawl:"https://www.firecrawl.dev/app/api-keys",brave:"https://api-dashboard.search.brave.com/app/keys",tavily:"https://app.tavily.com/",exa:"https://dashboard.exa.ai/api-keys"};
  const selectedChannel = channels.find(c=>c.provider===editing);
  const statusLabel = (status: string): string => ({ok:"成功", empty:"无匹配结果", unavailable:"搜索不可用", quota_exhausted:"额度耗尽", authentication_failed:"认证失败或访问被拒绝", rate_limited:"请求限流", challenge:"需要网站验证", network_error:"网络连接失败", invalid_response:"响应无法解析", http_error:"服务响应失败"}[status] ?? (status.startsWith("cooldown:") ? `冷却中：${statusLabel(status.slice(9))}` : status));
  const load = () => backend<SearchChannel[]>("ModelFacade", "ListSearchChannels").then(setChannels);
  useEffect(() => { let active = true; void backend<SearchChannel[]>("ModelFacade", "ListSearchChannels").then(v => {if (active) setChannels(v);}).catch(e => {if (active) setMessage(errorText(e));}); return () => {active = false;}; }, []);
  async function saveChannel(c: SearchChannel) {
    setBusy(true); setMessage("");
    try {await backend<void>("ModelFacade", "SaveSearchChannel", {...c, apiKey: keys[c.provider] ?? ""}); setKeys(k => ({...k,[c.provider]:""})); setEditing(""); await load(); setMessage(`${labels[c.provider]} 已保存`);} catch(e) {setMessage(errorText(e));} finally {setBusy(false);}
  }
  async function remove(id: string) {
    if (!confirmKeyDelete) {setConfirmKeyDelete(true);return;}
    setConfirmKeyDelete(false);
    setBusy(true); try {await backend<void>("ModelFacade", "DeleteSearchChannel", id); setKeys(k => ({...k,[id]:""})); await load(); setMessage("已删除");} catch(e) {setMessage(errorText(e));} finally {setBusy(false);}
  }
  async function test() {
    setBusy(true);setMessage("");setResult(null);
    try {setResult(await backend("ModelFacade", "TestWebSearch", query));} catch(e) {setMessage(errorText(e));} finally {setBusy(false);}
  }
  async function reorder(from: string, to: string) {
    setDragging(""); setDropTarget("");
    if (busy || from === to) return;
    const start = channels.findIndex(c=>c.provider===from), end = channels.findIndex(c=>c.provider===to);
    if (start < 0 || end < 0) return;
    const before = channels;
    const next = [...channels]; const moved = next.splice(start,1)[0]; if (!moved) return; next.splice(end,0,moved);
    const positions = new Map<string,number>();
    listRef.current?.querySelectorAll<HTMLElement>("[data-provider]").forEach(el=>positions.set(el.dataset.provider!,el.getBoundingClientRect().top));
    setChannels(next.map((c,i)=>({...c,priority:i+1}))); setBusy(true); setMessage("");
    requestAnimationFrame(()=>{
      if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
      listRef.current?.querySelectorAll<HTMLElement>("[data-provider]").forEach(el=>{
        const delta=(positions.get(el.dataset.provider!) ?? el.getBoundingClientRect().top)-el.getBoundingClientRect().top;
        if(delta) el.animate([{transform:`translateY(${delta}px)`},{transform:"translateY(0)"}],{duration:240,easing:"cubic-bezier(.2,.8,.2,1)"});
      });
    });
    try {await backend<void>("ModelFacade","SaveSearchOrder",next.map(c=>c.provider));setMessage("搜索顺序已保存");}
    catch(e) {listRef.current?.getAnimations({subtree:true}).forEach(a=>a.cancel());setChannels(before);setMessage(errorText(e));} finally {setBusy(false);}
  }
  return <div className="web-search-settings">
    <header className="web-search-heading"><h3><Icon name="search" size={22}/>联网搜索</h3><span>拖拽调整顺序</span></header>
    <div className="web-search-channels" ref={listRef}>{channels.map((c, index) => <section className={`web-search-channel ${dragging===c.provider ? "is-dragging" : ""} ${dropTarget===c.provider ? "is-drop-target" : ""}`} key={c.provider} data-provider={c.provider} onDragOver={e=>{if(dragging && !busy){e.preventDefault();e.dataTransfer.dropEffect="move";setDropTarget(c.provider);}}} onDrop={e=>{e.preventDefault();void reorder(dragging,c.provider);}}>
      <div className="web-search-channel-row">
        <button type="button" className="web-search-drag" draggable={!busy} disabled={busy} title="拖动排序；方向键上下移动" aria-label={`调整 ${labels[c.provider]} 顺序`} onDragStart={e=>{setDragging(c.provider);e.dataTransfer.effectAllowed="move";e.dataTransfer.setData("text/plain",c.provider);const card=e.currentTarget.closest("section");if(card)e.dataTransfer.setDragImage(card,24,30);}} onDragEnd={()=>{setDragging("");setDropTarget("");}} onKeyDown={e=>{if(e.key==="ArrowUp" || e.key==="ArrowDown"){e.preventDefault();const target=channels[index+(e.key==="ArrowUp"?-1:1)];if(target)void reorder(c.provider,target.provider);}}}><span aria-hidden="true" className="web-search-grip"/></button>
        <span className={`web-search-rank ${c.configured ? "is-configured" : ""}`} title={c.configured ? "Key 已配置" : "未配置 Key"} aria-label={`优先级 ${index+1}，${c.configured ? "Key 已配置" : "未配置 Key"}`}>{index + 1}</span>
        <header><b>{labels[c.provider]}</b></header>
        <label className="web-search-toggle"><input type="checkbox" role="switch" aria-label={`启用 ${labels[c.provider]}`} checked={c.enabled} disabled={busy} onChange={e => {if (!c.configured) {setEditing(c.provider);setMessage("请先配置 API Key");return;} void saveChannel({...c,enabled:e.target.checked});}}/>{c.enabled ? "已启用" : "未启用"}</label>
        <button type="button" aria-haspopup="dialog" disabled={busy} onClick={() => {setKeys({});setConfirmKeyDelete(false);setMessage("");setEditing(c.provider);}}><Icon name="settings" size={15}/>配置 Key</button>
      </div>
    </section>)}
      <section className="web-search-channel web-search-fallback"><span className="web-search-rank">{channels.length+1}</span><header><b>DuckDuckGo</b><span>无需 Key</span></header><span className="web-search-fallback-status">最终兜底</span><Icon name="search" size={20}/></section>
    </div>
    <SearchNetworkStatus service={backend} openNetwork={openNetwork}/>
    <footer className="web-search-footer"><span role="status">{message}</span><button type="button" disabled={busy} onClick={()=>setTesting(true)}><Icon name="search" size={16}/>测试搜索</button></footer>
    {selectedChannel && <WebSearchTestDialog busy={busy} dirty={Boolean(keys[editing])} title={`${labels[editing]} · 配置 Key`} close={()=>{if(!busy){setEditing("");setKeys({});setMessage("");}}}>
      <form className="web-search-key-dialog" onSubmit={e=>{e.preventDefault();void saveChannel({...selectedChannel,enabled:selectedChannel.configured ? selectedChannel.enabled : true});}}>
        <div className="web-search-key-source"><span>配置来源</span><a href={keySources[editing]} onClick={e=>{e.preventDefault();try{openDefaultBrowser(e.currentTarget.href);}catch(error){setMessage(errorText(error));}}}>{labels[editing]} 密钥管理 <Icon name="back" size={14}/></a></div>
        <label>API Key<input autoFocus type="password" autoComplete="new-password" value={keys[editing] ?? ""} placeholder={selectedChannel.configured ? "留空保留现有 Key" : "输入 API Key"} disabled={busy} onChange={e=>setKeys({[editing]:e.target.value})}/></label>
        {message && <p role="status">{message}</p>}
        {confirmKeyDelete && <p role="alert">确定删除此供应商的 Key 并停用？</p>}
        <footer><button type="button" disabled={busy || !selectedChannel.configured} onClick={()=>void remove(editing)}><Icon name="trash" size={14}/>{confirmKeyDelete ? "确认删除" : "删除配置"}</button>{confirmKeyDelete && <button type="button" onClick={()=>setConfirmKeyDelete(false)}>取消</button>}<button className="web-search-key-save" type="submit" disabled={busy || (!selectedChannel.configured && !keys[editing]?.trim())}><Icon name="check" size={14}/>保存</button></footer>
      </form>
    </WebSearchTestDialog>}
    {testing && <WebSearchTestDialog busy={busy} close={()=>{if(!busy)setTesting(false);}}>
      <form className="web-search-test" onSubmit={e => {e.preventDefault();void test();}}><label>测试查询<input autoFocus value={query} maxLength={500} disabled={busy} onChange={e => setQuery(e.target.value)}/></label><button disabled={busy || !query.trim()} type="submit"><Icon name="search" size={14}/>{busy ? "搜索中" : "开始测试"}</button></form>
      {message && <p role="status">{message}</p>}
      {result && <section className="web-search-result"><strong>{statusLabel(result.status)}{result.provider ? ` · ${labels[result.provider]}` : ""}</strong>{result.attempts.map((a,i) => <div key={i}>{labels[a.provider]}：{a.status === "query_too_long" ? "查询超出该来源长度限制，已尝试后续来源" : statusLabel(a.status)}</div>)}{result.items.map(i => <p key={i.url}><a href={i.url} target="_blank" rel="noreferrer">{i.title || i.url}</a></p>)}</section>}
    </WebSearchTestDialog>}
  </div>;
}

function WebSearchTestDialog({close, children, title="测试搜索",busy=false,dirty=false}: {close:()=>void; children:ReactNode; title?:string;busy?:boolean;dirty?:boolean}) {
  return createPortal(<ModalDialog className="web-search-test-dialog" close={close} busy={busy} dirty={dirty} aria-label={title}><header><h3>{title}</h3><button type="button" data-dialog-dismiss aria-label={`关闭${title}`}><Icon name="close"/></button></header>{children}</ModalDialog>,document.body);
}

function VisionFallbackSettings() {
  const [channels, setChannels] = useState<VisionFallbackChannel[]>([]);
  const [editingId, setEditingId] = useState("");
  const [draft, setDraft] = useState<VisionFallbackDraft>(newVisionFallbackDraft);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [testingId, setTestingId] = useState("");
  const [feedback, setFeedback] = useState<{ kind: "ok" | "error" | "info"; text: string } | null>(null);

	const selected = channels.find((channel) => channel.id === editingId);

  const savedDraft=useRef(newVisionFallbackDraft());
  useDialogGuard({busy:busy||Boolean(testingId),dirty:JSON.stringify(draft)!==JSON.stringify(savedDraft.current)});
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
      savedDraft.current=newVisionFallbackDraft();setDraft(savedDraft.current);
    } else {
      setEditingId(channel.id);
      const next={ name: channel.name, apiProtocol: channel.apiProtocol, baseUrl: channel.baseUrl ?? "", modelId: channel.modelId, apiKey: "", priority: channel.priority, enabled: channel.enabled, timeoutSeconds: channel.timeoutSeconds || 60, maxTokens: channel.maxTokens || 4096 };savedDraft.current=next;setDraft(next);
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
      savedDraft.current={...draft,apiKey:""};setDraft(savedDraft.current);
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
    if (!editingId || !await appConfirm({ title: "删除识图兜底渠道？", message: "这个自定义渠道及其系统凭据会一并删除。", confirmLabel: "删除渠道", tone: "danger" })) return;
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

function workflowRunStatusLabel(run: WorkflowRun) {
	if (run.workflowPurpose === "research_starter" && run.status === "completed") return "路线规划已完成";
	if (run.status === "completed" && run.outputs?.research_design && typeof run.outputs.research_design === "object" && !Array.isArray(run.outputs.research_design) && (run.outputs.research_design as Record<string, unknown>).status === "research_design_not_empirical_result") return "研究设计已完成";
	return workflowRunLabels[run.status];
}

const workflowStepLabels: Record<WorkflowStepStatus, string> = {
  queued: "待执行", running: "执行中", waiting_approval: "等待授权", waiting_human_confirmation: "等待人工确认",
  completed: "已完成", failed: "失败", cancelled: "已取消", interrupted: "已中断", outcome_unknown: "结果未知",
};

const researchStageLabels: Record<string, string> = {
  question_refinement: "澄清研究问题",
  literature_discovery: "发现相关文献",
  candidate_review: "筛选文献候选",
  evidence_extraction: "提取与冻结证据",
	method_selection: "综合方法 Skill",
	research_design: "形成研究设计",
	data_preflight: "检查研究数据",
	method_implementation: "实现分析方法",
	dependency_preparation: "准备分析依赖",
	python_analysis: "执行可复现分析",
	result_interpretation: "解释结果与局限",
	report_drafting: "形成交付稿",
  independent_review: "独立二次审查",
  delivery_gate: "核验交付条件",
  report_publication: "发布可信报告",
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

const workflowCandidateScreening = (step: WorkflowStep): CandidateScreening | null => {
  const value = step.input?.screening;
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const screening = value as Partial<CandidateScreening>;
  if (!Array.isArray(screening.recommendedCandidateIds) || !Array.isArray(screening.candidateAssessments) || !screening.coverage || typeof screening.coverage !== "object") return null;
  return screening as CandidateScreening;
};

const workflowEvidenceScreening = (step: WorkflowStep): EvidenceScreening | null => {
  const value = step.input?.screening;
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const screening = value as Partial<EvidenceScreening>;
  if (!Array.isArray(screening.recommendedReferences) || !Array.isArray(screening.citationAssessments) || !screening.coverage || typeof screening.coverage !== "object") return null;
  return screening as EvidenceScreening;
};

const workflowEvidenceSelectionNeedsAcceptance = (step: WorkflowStep, selectedCitationKeys: string[]) => {
  const screening = workflowEvidenceScreening(step);
  if (!screening) return false;
  const selectedCitations = selectedCitationKeys.map((key) => {
    try { return JSON.parse(key) as WorkflowCitation; } catch { return null; }
  }).filter((value): value is WorkflowCitation => Boolean(value));
  const selected = new Set(selectedCitations.map((value) => String(value.reference ?? "").trim()).filter(Boolean));
  const studies = new Set(selectedCitations.map((value) => value.documentId?.trim() ? `document:${value.documentId.trim()}` : value.attachmentId?.trim() ? `attachment:${value.attachmentId.trim()}` : `source:${String(value.sourceName ?? value.id).trim().toLowerCase()}`));
  const fullTextObserved = selectedCitations.some((value) => {
    const source = String(value.sourceName ?? "").trim().toLowerCase();
    const mime = String(value.mimeType ?? "").trim().toLowerCase();
    const quote = String(value.quote ?? "").trim();
    const disclosure = quote.toLowerCase().includes("metadata") && quote.toLowerCase().includes("not the publication full text");
    const metadataMaterial = disclosure || source.includes("-metadata.") || source.includes("_metadata.");
    return !metadataMaterial && Boolean(quote) && (mime === "application/pdf" || source.endsWith(".pdf") || mime === "text/markdown" || mime === "text/plain");
  });
  return screening.coverage.sufficientForClaimedScope !== true
    || screening.recommendedReferences.some((reference) => !selected.has(reference.trim()))
    || screening.recommendedReferences.length === 0
    || studies.size < 2
    || !fullTextObserved;
};

const workflowHash = (value?: string) => value ? value.slice(0, 12) : "未提交";

function workflowResearchDesign(detail: WorkflowRunDetail): WorkflowResearchDesign | null {
  if (detail.run.status !== "completed" || detail.deliveryAssessment?.status !== "reviewed") return null;
  const value = detail.run.outputs?.research_design;
  const gate = detail.run.outputs?.delivery_gate;
  if (!value || typeof value !== "object" || Array.isArray(value) || !gate || typeof gate !== "object" || Array.isArray(gate) || (gate as Record<string, unknown>).approved !== true) return null;
  const design = value as Partial<WorkflowResearchDesign>;
  if (design.status !== "research_design_not_empirical_result" || typeof design.title !== "string" || !design.title.trim() || typeof design.researchQuestion !== "string" || !design.researchQuestion.trim()) return null;
  const list = (input: unknown) => Array.isArray(input) ? input.filter((item): item is string => typeof item === "string" && Boolean(item.trim())) : [];
  return {
    title: design.title.trim(), researchQuestion: design.researchQuestion.trim(), status: design.status,
    objectives: list(design.objectives), hypotheses: list(design.hypotheses), populationAndSampling: list(design.populationAndSampling),
    variablesOrMaterials: list(design.variablesOrMaterials), dataCollectionPlan: list(design.dataCollectionPlan), analysisPlan: list(design.analysisPlan),
    qualityControls: list(design.qualityControls), ethicsAndRisks: list(design.ethicsAndRisks), milestones: list(design.milestones),
    evidenceGaps: list(design.evidenceGaps), limitations: list(design.limitations),
  };
}

function workflowResearchReport(detail: WorkflowRunDetail): WorkflowResearchReport | null {
  if (detail.run.status !== "completed" || detail.deliveryAssessment?.status !== "reviewed") return null;
  const value = detail.run.outputs?.report_draft;
  const gate = detail.run.outputs?.delivery_gate;
  if (!value || typeof value !== "object" || Array.isArray(value) || !gate || typeof gate !== "object" || Array.isArray(gate) || (gate as Record<string, unknown>).approved !== true) return null;
  const report = value as Partial<WorkflowResearchReport>;
  const list = (input: unknown) => Array.isArray(input) ? input.filter((item): item is string => typeof item === "string" && Boolean(item.trim())) : [];
  if (typeof report.markdown !== "string" || !report.markdown.trim() || typeof report.methodSummary !== "string" || !report.methodSummary.trim() || !["low", "medium", "high"].includes(String(report.confidence))) return null;
  return { markdown: report.markdown.trim(), methodSummary: report.methodSummary.trim(), claimSummary: list(report.claimSummary), limitations: list(report.limitations), confidence: report.confidence as WorkflowResearchReport["confidence"] };
}

function workflowDeliverableRegistered(detail: WorkflowRunDetail, outputName: "research_design" | "report_draft") {
  return detail.registeredDeliverables?.includes(outputName) === true;
}

function workflowRevisionConversationId(detail: WorkflowRunDetail | null): string {
  if (!detail || detail.run.workflowPurpose === "research_starter" || workflowDeliverableRegistered(detail, "report_draft") || !workflowResearchReport(detail)) return "";
  return detail.run.conversationId ?? "";
}

const workflowResearchDesignSections = (design: WorkflowResearchDesign) => [
  ["研究目标", design.objectives], ["研究假设", design.hypotheses], ["研究人群与采样", design.populationAndSampling],
  ["变量与材料", design.variablesOrMaterials], ["数据采集计划", design.dataCollectionPlan], ["分析计划", design.analysisPlan],
  ["质量控制", design.qualityControls], ["伦理与风险", design.ethicsAndRisks], ["实施里程碑", design.milestones],
  ["证据缺口", design.evidenceGaps], ["局限", design.limitations],
] as const;

function WorkflowRunEvidence({ detail, openArtifacts }: { detail: WorkflowRunDetail; openArtifacts: (taskId?: string) => void }) {
  const evidence = deriveWorkflowRunEvidence(detail);
  const analysis = evidence.analyses.at(-1);
  const parents = (node: WorkflowEvidenceNode) => evidence.graph.edges
    .filter((edge) => edge.to === node.id)
    .map((edge) => ({ ...edge, source: evidence.graph.nodes.find((candidate) => candidate.id === edge.from) }))
    .filter((edge) => edge.source);
  const stages: { id: WorkflowEvidenceNode["stage"][]; label: string; description: string }[] = [
    { id: ["source", "evidence"], label: "文献来源与可信证据", description: "区分发现来源与本地 Citation 快照" },
    { id: ["analysis", "artifact"], label: "分析与中间产物", description: "由可复现步骤生成" },
    { id: ["report"], label: "研究成果", description: "汇总分析与核验后形成" },
    { id: ["export"], label: "发布版本", description: "从正式成果确定性导出" },
  ];
  return <section className="workflow-run-evidence">
    <div className="workflow-run-evidence-grid">
      <article className="workflow-environment-manifest">
        <details>
          <summary><header><div><Icon name="tool" size={15}/><span><b>复现快照</b><small>实际提交的环境、依赖与 Kernel 指纹</small></span></div><em>{evidence.environment ? "已冻结" : "等待环境"}</em></header></summary>
        {evidence.environment ? <dl>
          <div><dt>解释器</dt><dd>{[evidence.environment.implementation, evidence.environment.version, evidence.environment.architecture].filter(Boolean).join(" · ")}</dd></div>
          <div><dt>环境指纹</dt><dd><code title={evidence.environment.environmentFingerprint}>{workflowHash(evidence.environment.environmentFingerprint)}</code></dd></div>
          <div><dt>依赖冻结</dt><dd><code title={evidence.environment.freezeSha256}>{workflowHash(evidence.environment.freezeSha256)}</code><span>{evidence.environment.lockCount} 个包</span></dd></div>
          <div><dt>基础解释器</dt><dd><code title={evidence.environment.baseExecutableSha256}>{workflowHash(evidence.environment.baseExecutableSha256)}</code></dd></div>
          <div><dt>Kernel 复现</dt><dd>{analysis ? <><code title={analysis.reproductionSha256}>{workflowHash(analysis.reproductionSha256)}</code><span>{analysis.inputHashes.length} 输入 · {analysis.outputHashes.length} 输出</span></> : <span>等待分析步骤提交</span>}</dd></div>
        </dl> : <p>环境步骤尚未完成；SciAide 不会从当前电脑配置推测本次 Run 的环境。</p>}
        </details>
      </article>
      <article className="workflow-artifact-lineage">
        <header><div><Icon name="archive" size={15}/><span><b>成果来源链</b><small>按“来源 → 分析 → 成果 → 发布”阅读</small></span></div>{detail.artifactCount > 0 && <button type="button" onClick={() => openArtifacts(detail.run.researchTaskId)}>打开产物库 · {detail.artifactCount}</button>}</header>
        {evidence.graph.nodes.length ? <div className="workflow-artifact-flow">{stages.map((stage, stageIndex) => {
          const nodes = evidence.graph.nodes.filter((node) => stage.id.includes(node.stage));
          return <section className={nodes.length ? "populated" : "empty"} key={stage.label}>
            <header><i>{stageIndex + 1}</i><span><b>{stage.label}</b><small>{nodes.length ? `${nodes.length} 项 · ${stage.description}` : `等待生成 · ${stage.description}`}</small></span></header>
            {nodes.length > 0 && <div>{nodes.map((node) => {
              const upstream = parents(node);
              const tooltip = [
                node.detail || node.sourceStepName,
                node.sha256 ? `SHA256 ${node.sha256}` : "",
                ...upstream.map((edge) => `来自：${edge.source?.label} · ${edge.label}`),
              ].filter(Boolean).join("\n");
              return <article className={`workflow-evidence-node ${node.stage}`} key={node.id} title={tooltip || node.label}>
                <b>{node.label}</b>
              </article>;
            })}</div>}
          </section>;
        })}</div> : <p>尚无已提交证据或产物；这里只展示运行真实生成并冻结的结果，不创建占位节点。</p>}
      </article>
    </div>
  </section>;
}

function LiteratureSourceAbstract({projectId, step, candidate}: {projectId: string; step: WorkflowStep; candidate: WorkflowCandidate}) {
  const [content,setContent] = useState(candidate.abstract || "");
  const [state,setState] = useState<"idle" | "loading" | "ready" | "error">(candidate.abstract ? "ready" : "idle");
  const [error,setError] = useState("");
  const requests = useRef(createLatestRequestGate());
  useEffect(() => () => {requests.current.invalidate();},[]);
  async function read() {
    if (state === "loading" || state === "ready" || !projectId) return;
    const request = requests.current.begin();setState("loading");
    try {
      const value = await backend<WorkflowCandidate>("WorkflowFacade","ReadLiteratureCandidate",projectId,step.workflowRunId,step.id,candidate.id);
      if (!requests.current.isCurrent(request)) return;
      setContent(value.abstract || "当前来源未提供摘要。");setState("ready");
    } catch(error) {if(requests.current.isCurrent(request)){setError(errorText(error));setState("error");}}
  }
  return <details className="literature-source-abstract" onClick={(event) => event.stopPropagation()} onToggle={(event) => {if(event.currentTarget.open) void read();}}><summary>查看原始摘要</summary><p>{state === "loading" ? "正在读取原始摘要..." : state === "error" ? error : content}</p></details>;
}

function WorkflowHumanDecision({ projectId = "", taskId = "", supportsMaterials = false, selectedAttachmentIds, setSelectedAttachmentIds, step, prompt, note, context, selectedCandidateIds, selectedCitationKeys, acceptLimitedEvidence, busy, setNote, setContext, setSelectedCandidateIds, setSelectedCitationKeys, setAcceptLimitedEvidence, decide, allowEmptyCitations = false }: {
  taskId?: string;
  supportsMaterials?: boolean;
  selectedAttachmentIds: string[];
  setSelectedAttachmentIds: (ids: string[]) => void;
  projectId?: string;
  step: WorkflowStep;
  prompt?: string;
  note: string;
  context: string;
  selectedCandidateIds: string[];
  selectedCitationKeys: string[];
  acceptLimitedEvidence: boolean;
  busy: string;
  setNote: (value: string) => void;
  setContext: (value: string) => void;
  setSelectedCandidateIds: (value: string[]) => void;
  setSelectedCitationKeys: (value: string[]) => void;
	setAcceptLimitedEvidence: (value: boolean) => void;
	decide: (approved: boolean, continueWithoutCitations?: boolean, acceptLimitedEvidence?: boolean) => void;
	allowEmptyCitations?: boolean;
}) {
  const candidates = workflowCandidates(step);
  const [materialBusy, setMaterialBusy] = useState(false);
  const citations = workflowCitations(step);
  const toggle = (values: string[], value: string, update: (next: string[]) => void) => update(values.includes(value) ? values.filter((item) => item !== value) : [...values, value]);
  const citationKey = (value: WorkflowCitation) => JSON.stringify(value);
  const candidateScreening = step.nodeKind === "candidate_selection" ? workflowCandidateScreening(step) : null;
  const evidenceScreening = step.nodeKind === "citation_selection" ? workflowEvidenceScreening(step) : null;
  const candidateAssessment = new Map(candidateScreening?.candidateAssessments.map((value) => [value.candidateId, value]) ?? []);
  const citationAssessment = new Map(evidenceScreening?.citationAssessments.map((value) => [value.reference, value]) ?? []);
  const recommendedCandidates = new Set(candidateScreening?.recommendedCandidateIds ?? []);
  const recommendedReferences = new Set(evidenceScreening?.recommendedReferences ?? []);
  const visibleCandidates = [...candidates].sort((left, right) => Number(recommendedCandidates.has(right.id)) - Number(recommendedCandidates.has(left.id)));
  const visibleCitations = [...citations].sort((left, right) => Number(recommendedReferences.has(right.reference ?? "")) - Number(recommendedReferences.has(left.reference ?? "")));
  const selection = step.nodeKind === "candidate_selection" || step.nodeKind === "citation_selection";
  const agentReview = step.nodeKind === "agent_stage";
  const implementationReview = agentReview && step.nodeId === "method_implementation" ? step.output?.implementationReview as {changes?: Array<{before: string; after: string; reason: string; impact: string}>} | undefined : undefined;
  const selectedCount = step.nodeKind === "candidate_selection" ? selectedCandidateIds.length + selectedAttachmentIds.length : selectedCitationKeys.length;
  const totalCount = step.nodeKind === "candidate_selection" ? candidates.length + selectedAttachmentIds.length : citations.length;
	const emptyCitationRecovery = step.nodeKind === "citation_selection" && citations.length === 0 && allowEmptyCitations;
  const limitedEvidenceAcceptanceRequired = step.nodeKind === "citation_selection" && !emptyCitationRecovery && workflowEvidenceSelectionNeedsAcceptance(step, selectedCitationKeys);
  const coverage = candidateScreening?.coverage ?? evidenceScreening?.coverage;
  const retrievalStatus = candidateScreening?.retrieval?.status;
  const coverageLabel = retrievalStatus === "source_blocked" ? "部分来源检索受阻" : retrievalStatus === "budget_exhausted" ? "检索尚未充分完成" : coverage?.strength === "adequate" ? "覆盖充分" : coverage?.strength === "limited" ? "覆盖有限" : coverage ? "当前材料覆盖不足" : "";
  const screeningSummary = candidateScreening?.summary ?? evidenceScreening?.summary;
  const screeningGaps = candidateScreening?.coverage.gaps ?? evidenceScreening?.coverage.gaps ?? [];
  return <div className="workflow-human-decision">
    {supportsMaterials && step.nodeKind === "candidate_selection" && <ReferenceMaterials key={step.id} projectId={projectId} taskId={taskId} selected={selectedAttachmentIds} onChange={setSelectedAttachmentIds} disabled={Boolean(busy)} service={backend} onBusyChange={setMaterialBusy}/>}
    {Boolean(implementationReview?.changes?.length) && <section className="implementation-change-review"><h3>需要确认研究约定变化</h3>{implementationReview!.changes!.map((change, index) => <div key={index}><p><b>原约定：</b>{change.before}</p><p><b>拟调整：</b>{change.after}</p><p><b>原因：</b>{change.reason}</p><p><b>影响：</b>{change.impact}</p></div>)}</section>}
    <p>{implementationReview ? "本次修订涉及上述研究约定变化。确认后才会执行；这不是代码正确性的保证，执行后仍需独立审查。若不同意，请拒绝本次结果。" : agentReview ? "AI 已生成本阶段初稿。你可以在中间科研协作区追问、纠正或要求重写；确认时会采用该会话最新一条通过结构化校验的回答。" : prompt || (step.nodeKind === "candidate_selection" ? "请选择需要导入本地的文献候选。" : step.nodeKind === "citation_selection" ? "请选择报告使用的可信本地证据。" : "请确认后继续。")}</p>
    {screeningSummary && <section className={`workflow-ai-screening ${coverage?.strength ?? ""}`}><header><span><Icon name="spark" size={14}/><b>AI 筛选建议</b></span>{coverageLabel && <em>{coverageLabel}</em>}</header><p>{screeningSummary}</p>{screeningGaps.length > 0 && <details><summary>查看 {screeningGaps.length} 项证据缺口</summary><ul>{screeningGaps.map((gap, index) => <li key={`${gap}:${index}`}>{gap}</li>)}</ul></details>}</section>}
    {selection && <div className="workflow-selection-toolbar"><span>已选 {selectedCount} / {totalCount}</span><nav>{step.nodeKind === "candidate_selection" && candidateScreening && <button type="button" className="recommended" onClick={() => setSelectedCandidateIds(candidateScreening.recommendedCandidateIds.filter((id) => candidates.some((value) => value.id === id)))}>采用 AI 推荐</button>}{step.nodeKind === "citation_selection" && evidenceScreening && <button type="button" className="recommended" onClick={() => setSelectedCitationKeys(citations.filter((value) => recommendedReferences.has(value.reference ?? "")).map(citationKey))}>采用 AI 推荐</button>}<button type="button" onClick={() => step.nodeKind === "candidate_selection" ? setSelectedCandidateIds(candidates.map((value) => value.id)) : setSelectedCitationKeys(citations.map(citationKey))}>全选</button><button type="button" onClick={() => step.nodeKind === "candidate_selection" ? setSelectedCandidateIds([]) : setSelectedCitationKeys([])}>清空</button></nav></div>}
    {step.nodeKind === "candidate_selection" && <div className="workflow-selection-list">{candidates.length ? visibleCandidates.map((value) => {
      const checked = selectedCandidateIds.includes(value.id);
      const authors = (value.authors ?? []).map((author) => author.name).filter(Boolean).slice(0, 5).join("、");
      const assessment = candidateAssessment.get(value.id);
      const assessmentText = assessment ? [assessment.reason, assessment.purpose].filter(Boolean).join(" ") : "";
      const decision = assessment?.importAction === "direct" ? "推荐导入" : assessment?.importAction === "verify" ? "推荐核验" : assessment?.importAction === "background" ? "背景保留 · 不默认导入" : assessment?.decision === "core" ? "优先核验" : assessment?.decision === "support" ? "待核验材料" : assessment?.decision === "exclude" ? "AI 建议排除" : "";
      return <label key={value.id} className={`${checked ? "selected" : ""}${assessment?.decision === "exclude" ? " excluded" : ""}`}><input type="checkbox" checked={checked} onChange={() => toggle(selectedCandidateIds, value.id, setSelectedCandidateIds)}/><span><span className="workflow-selection-title"><b>{value.title || "未命名候选"}</b>{decision && <i className={assessment?.decision}>{decision}</i>}</span><small>{[authors, value.year, value.venue].filter(Boolean).join(" · ") || value.id}</small>{assessmentText && <p className="workflow-screening-reason">{assessmentText}</p>}{(value.abstract || value.sourceSha256 && projectId) && <LiteratureSourceAbstract key={`${step.id}:${value.id}:${value.sourceSha256 || ""}`} projectId={projectId} step={step} candidate={value}/>}<em>{value.sourceIds?.join(" / ") || "公共数据库"}{value.doi ? ` · DOI ${value.doi}` : ""}{value.openAccess ? " · 开放获取" : ""}</em></span></label>;
    }) : <div className="workflow-selection-empty">{supportsMaterials ? "当前没有数据库候选" : "没有可供筛选的候选，不能继续。"}</div>}</div>}
    {step.nodeKind === "citation_selection" && <div className="workflow-selection-list citations">{citations.length ? visibleCitations.map((value, index) => {
      const key = citationKey(value);
      const checked = selectedCitationKeys.includes(key);
      const assessment = citationAssessment.get(value.reference ?? "");
      const decision = assessment?.decision === "core" ? "核心摘录" : assessment?.decision === "support" ? "补充摘录" : assessment?.decision === "exclude" ? "AI 建议排除" : "";
      const sourceLevel = assessment?.sourceLevel === "full_text" ? "全文" : assessment?.sourceLevel === "abstract" ? "摘要" : assessment?.sourceLevel === "metadata" ? "仅元数据" : assessment?.sourceLevel === "mixed" ? "混合材料" : "";
      return <label key={`${value.reference || value.id}:${index}`} className={`${checked ? "selected" : ""}${assessment?.decision === "exclude" ? " excluded" : ""}`}><input type="checkbox" checked={checked} onChange={() => toggle(selectedCitationKeys, key, setSelectedCitationKeys)}/><span><span className="workflow-selection-title"><b>{value.title || value.sourceName || "本地证据"}</b>{decision && <i className={assessment?.decision}>{decision}</i>}</span><small>{[value.sourceName, value.locator, value.reference, sourceLevel].filter(Boolean).join(" · ")}</small>{assessment?.reason && <p className="workflow-screening-reason">{assessment.reason}</p>}<details><summary>查看原文摘录</summary><p>{value.quote || "无摘录"}</p></details></span></label>;
	}) : <div className={`workflow-selection-empty${emptyCitationRecovery ? " recoverable" : ""}`}>{emptyCitationRecovery ? <><b>本轮没有形成可引用的文献摘录</b><span>可以继续数据分析，但报告会明确记录“未建立外部证据链”，且不会生成任何文献引用。</span></> : "本地检索没有返回可引用证据；这条路线以文献证据为核心，不能空着继续。"}</div>}</div>}
    {limitedEvidenceAcceptanceRequired && <label className="workflow-limited-evidence"><input type="checkbox" checked={acceptLimitedEvidence} onChange={(event) => setAcceptLimitedEvidence(event.target.checked)}/><span><b>以有限证据继续</b><small>当前证据不足以完整覆盖原研究范围。继续后只能生成低置信、范围受限的初稿，不代表证据已充足。</small></span></label>}
    <label>备注<input value={note} onChange={(event) => setNote(event.target.value)} placeholder={selection ? "可选：记录选择依据" : "可选"}/></label>
    {step.nodeKind === "human_confirmation" && <label>决定上下文（JSON）<textarea rows={3} value={context} onChange={(event) => setContext(event.target.value)} /></label>}
	<footer><button type="button" onClick={() => decide(false)} disabled={Boolean(busy) || materialBusy}>{agentReview ? "拒绝阶段结果" : "拒绝"}</button><button type="button" className="accept" onClick={() => decide(true, emptyCitationRecovery, acceptLimitedEvidence)} disabled={Boolean(busy) || materialBusy || (step.nodeKind === "candidate_selection" && selectedCount > 100) || Boolean(selection && selectedCount === 0 && !emptyCitationRecovery) || Boolean(limitedEvidenceAcceptanceRequired && !acceptLimitedEvidence)}>{busy === `decide:${step.id}` ? "处理中…" : step.nodeKind === "candidate_selection" ? "导入所选研究材料" : emptyCitationRecovery ? "无文献证据，继续数据分析" : step.nodeKind === "citation_selection" ? limitedEvidenceAcceptanceRequired ? "确认限制并继续" : "确认 AI 推荐引用" : agentReview ? "确认并继续" : "确认继续"}</button></footer>
  </div>;
}

function workflowReviewIssues(detail: WorkflowRunDetail, gateStep: WorkflowStep): WorkflowReviewIssues | null {
  const reviewEdge = detail.run.compilation.edges.find((edge) => edge.toNode === gateStep.nodeId && edge.toPort === "review");
  const reviewStep = detail.steps.find((step) => step.nodeId === reviewEdge?.fromNode);
  const execution = reviewStep && [...detail.aiExecutions].reverse().find((value) => value.workflowStepId === reviewStep.id && value.attempt === reviewStep.attempt && value.status === "completed");
  if (!execution || !execution.output || typeof execution.output !== "object") return null;
  const output = execution.output as Record<string, unknown>;
  const fields = [
    ["unsupportedClaims", "证据与主张"], ["citationIssues", "引用"], ["numericIssues", "数字"], ["methodIssues", "方法"], ["requiredCorrections", "必须修正"],
  ] as const;
  const groups = fields.map(([key, label]) => ({ key, label, items: Array.isArray(output[key]) ? (output[key] as unknown[]).filter((item): item is string => typeof item === "string" && Boolean(item.trim())) : [] })).filter((group) => group.items.length > 0);
  return { approved: typeof output.approved === "boolean" ? output.approved : undefined, groups, total: groups.reduce((sum, group) => sum + group.items.length, 0), ...reviewTrackingModel(output) };
}

function researchRouteValidation(route: ResearchStarterRoute): ResearchRouteValidation {
  if (route.validation === "invalid" || route.validation === "blocked" || route.validation === "ready" || route.validation === "checking") return route.validation;
  return route.availableNow ? "ready" : "blocked";
}

function researchRouteCanAdopt(route: ResearchStarterRoute) {
  const validation = researchRouteValidation(route);
  // A blocked route is still a valid reusable plan: adopting it records the
  // route and lets the user provide its missing inputs later. Invalid and
  // still-checking routes must stay disabled because the backend will reject
  // them before a formal Workflow can be created.
  return validation !== "invalid" && validation !== "checking";
}

function researchRouteStatusLabel(route: ResearchStarterRoute) {
  const validation = researchRouteValidation(route);
  return validation === "ready" ? "可直接开始" : validation === "blocked" ? "需要补充资料" : validation === "checking" ? "正在核验" : "路线不可用";
}

const researchRouteInternalTermLabels: Array<[RegExp, string]> = [
  [/frozen_tabular_data/gi, "已冻结的数据快照"],
  [/frozen_python_environment/gi, "项目分析环境"],
  [/analysis_specification/gi, "分析方案"],
  [/analysis_code/gi, "分析代码"],
  [/analysis_artifacts/gi, "分析产物"],
  [/computed_results/gi, "计算结果"],
  [/delivery_draft/gi, "交付稿"],
  [/method_blueprint/gi, "方法方案"],
  [/research_question/gi, "研究问题"],
  [/route_context/gi, "研究路线信息"],
  [/starter_context/gi, "研究课题信息"],
  [/available_skills/gi, "可用科研方法"],
  [/skill[_ ]snapshot/gi, "科研方法记录"],
  [/tabularfiles/gi, "表格数据"],
  [/abularfiles/gi, "表格数据"],
  [/input_paths/gi, "数据文件"],
  [/question_refinement/gi, "研究问题"],
  [/literature_discovery/gi, "文献检索"],
  [/candidate_review/gi, "文献筛选"],
  [/evidence_extraction/gi, "证据提取"],
  [/method_selection/gi, "方法选择"],
  [/research_design/gi, "研究设计"],
  [/data_preflight/gi, "数据检查"],
  [/method_implementation/gi, "分析方法实现"],
  [/dependency_preparation/gi, "准备分析环境"],
  [/python_analysis/gi, "执行数据分析"],
  [/result_interpretation/gi, "解释分析结果"],
  [/report_drafting/gi, "形成报告"],
  [/independent_review/gi, "独立审查"],
  [/delivery_gate/gi, "交付核验"],
  [/report_publication/gi, "发布报告"],
  [/stageids/gi, "阶段"],
  [/workflow\s*inputs?/gi, "任务输入"],
];

function researchRouteUserText(value: string | undefined): string {
  let text = (value ?? "").trim();
  if (!text) return "";
  for (const [pattern, replacement] of researchRouteInternalTermLabels) text = text.replace(pattern, replacement);
  text = text
    .replace(/\bworkspace\b/gi, "当前项目")
    .replace(/\bresearch\s*connectors?\b/gi, "文献检索来源")
    .replace(/\bconnectors?\b/gi, "文献检索来源")
    .replace(/当前项目\s*(?:当前)?为空[^；。]*无法在\s*证据提取\s*中立即引用本地材料/gi, "当前没有可引用的文献材料")
    .replace(/(?:文献检索|发现相关文献)\s*必须使用文献检索来源检索/gi, "需要先检索相关文献")
    .replace(/无法在\s*证据提取\s*中立即引用本地材料/gi, "当前没有可引用的文献材料");
  // Do not expose JSON/property notation copied from a planner response.
  text = text.replace(/\$\.[A-Za-z0-9_.-]+/g, "").replace(/\b(?:route|stage|node|port)\.[A-Za-z0-9_.-]+\b/gi, "");
  return text.replace(/\s{2,}/g, " ").trim();
}

// Route validation keeps internal stage and snapshot names for auditability,
// but those names are not actionable to a user. Keep the translation at the
// display boundary so the backend can still retain its precise diagnostics.
function researchRouteUserGap(route: ResearchStarterRoute): string | null {
  const values = [...(route.blockers ?? []), route.validationError ?? ""]
    .map((value) => value.trim())
    .filter(Boolean);
  if (values.length === 0) return null;
  const combined = values.join(" ").toLowerCase();
  if (/(tabularfiles|abularfiles|frozen_tabular_data|data_preflight|input_paths|表格数据|csv|tsv|xlsx)/i.test(combined)) {
    return "请上传或选择一份与本课题相关的 CSV、TSV 或 XLSX 数据文件。";
  }
  if (/(workspace\s*(?:当前)?为空|无法在\s*(?:evidence[_ ]extraction|证据提取)\s*中|literature[_ ]discovery|文献检索|evidence[_ ]extraction|证据提取|connectors?|本地材料|可引用(?:的)?文献|引用证据)/i.test(combined)) {
    return "当前没有可引用的文献材料，请先检索并选择与本课题相关的文献。";
  }
  if (/(stageids|route_context|starter_context|research_question|available_skills|skill snapshot|快照|阶段依赖|阶段顺序|无效或重复的阶段|方法 skill 名称)/i.test(combined)) {
    return null;
  }
  // Unknown diagnostics are intentionally hidden. They remain available in
  // the run audit, but a raw planner/host string is not a user instruction.
  return null;
}

function ResearchRoutePlanningNotes({ route }: { route: ResearchStarterRoute }) {
  const notes = (route.planningNotes ?? []).map(researchRouteUserText).filter(Boolean);
  if (researchRouteValidation(route) !== "ready" && notes.length === 0) return null;
  return <details className="research-route-planning-notes">
    <summary>研究边界与后续条件</summary>
    <p>可开始仅表示能够执行本轮路线，不代表已经获得实证结论或满足招募、授权与伦理审批条件。</p>
    {notes.length > 0 && <ul>{notes.map((note, index) => <li key={`${index}:${note}`}>{note}</li>)}</ul>}
  </details>;
}

function ResearchRouteDetailDialog({ route, plan, busy, close, adopt }: { route: ResearchStarterRoute; plan: ResearchStarterPlan; busy: boolean; close: () => void; adopt: (route: ResearchStarterRoute) => void }) {
  // The dialog may stay open while the host validation request completes.
  // Resolve the route again from the authoritative plan so its status cannot
  // become stale after the list has been updated.
  const currentRoute = plan.routes.find((value) => value.routeId === route.routeId) ?? route;
  const validation = researchRouteValidation(currentRoute);
  const availableNow = validation === "ready";
  const canAdopt = researchRouteCanAdopt(currentRoute);
  const userGap = researchRouteUserGap(currentRoute);
  const usedSkillNames = new Set(currentRoute.layers.flatMap((layer) => layer.stages.flatMap((stage) => (stage.skillNames ?? []))));
  const usedSkills = (plan.selectedSkills ?? []).filter((skill) => usedSkillNames.has(skill.name));

  return createPortal(<ModalBackdrop className="modal-backdrop research-route-detail-backdrop" close={close} busy={busy}>
    <section className="model-modal research-route-detail-dialog" role="dialog" aria-modal="true" aria-labelledby="research-route-detail-title">
      <header><div><span className="dialog-icon"><Icon name="library" size={19}/></span><div><p>研究路线详情</p><h2 id="research-route-detail-title">{researchRouteUserText(route.title)}</h2></div></div><button type="button" className="close" disabled={busy} data-dialog-dismiss onClick={close} aria-label="关闭路线详情"><Icon name="close"/></button></header>
      <div className="research-route-detail-body">
        <section className="research-route-detail-summary"><div><em className={validation === "ready" ? "ready" : validation === "blocked" ? "blocked" : validation === "checking" ? "checking" : "invalid"}>{researchRouteStatusLabel(currentRoute)}</em>{currentRoute.routeId === plan.recommendedRouteId && <strong>AI 推荐</strong>}</div><p>{researchRouteUserText(currentRoute.reason)}</p>{userGap && <p className="research-route-validation-error">开始前需要：{userGap}</p>}{validation === "checking" && <p className="research-route-validation-checking">正在核对路线条件…</p>}<nav><span><b>{currentRoute.layers.length}</b> 个层次</span><span><b>{currentRoute.stageIds.length}</b> 个阶段</span><span><b>{currentRoute.deliverables.length}</b> 项交付</span><span><b>{usedSkills.length}</b> 个科研 Skill</span></nav></section>
        <ResearchRoutePlanningNotes route={currentRoute}/>
        <section className="research-route-detail-section"><header><h3>分层执行路线</h3><small>按阶段顺序推进</small></header><div className="research-route-detail-layers">{currentRoute.layers.map((layer, layerIndex) => <section key={layer.layerId}><header><i>{layerIndex + 1}</i><span><b>{researchRouteUserText(layer.title)}</b><small>{researchRouteUserText(layer.objective)}</small></span></header><ol>{layer.stages.map((stage) => <li key={stage.stageId}><header><b>{researchStageLabels[stage.stageId] ?? researchRouteUserText(stage.stageId)}</b>{stage.humanCheckpoint && <em>人工检查</em>}</header><p>{researchRouteUserText(stage.objective)}</p>{(stage.methods ?? []).length > 0 && <dl><dt>方法</dt><dd>{(stage.methods ?? []).map(researchRouteUserText).filter(Boolean).join(" · ")}</dd></dl>}{(stage.skillNames ?? []).length > 0 && <dl><dt>科研 Skill</dt><dd>{(stage.skillNames ?? []).map(researchRouteUserText).filter(Boolean).join(" · ")}</dd></dl>}<dl><dt>输入</dt><dd>{(stage.inputs ?? []).length ? (stage.inputs ?? []).map(researchRouteUserText).filter(Boolean).join(" · ") : "由前序阶段提供"}</dd></dl><dl><dt>输出</dt><dd>{(stage.outputs ?? []).map(researchRouteUserText).filter(Boolean).join(" · ")}</dd></dl></li>)}</ol></section>)}</div></section>
        {usedSkills.length > 0 && <section className="research-route-detail-section"><header><h3>采用的研究 Skill</h3><small>方案声明 {usedSkills.length} 个；其中 {usedSkills.filter((skill) => plan.loadedSkills?.some((loaded) => loaded.name === skill.name)).length} 个已核验加载</small></header><div className="research-route-detail-skills">{usedSkills.map((skill) => <div key={skill.name}><b>{researchRouteUserText(skill.name)}</b><p>{researchRouteUserText(skill.role)}</p>{(skill.limitations ?? []).length > 0 && <small>局限：{(skill.limitations ?? []).map(researchRouteUserText).filter(Boolean).join("；")}</small>}</div>)}</div></section>}
        <section className="research-route-detail-section research-route-detail-requirements"><header><h3>交付与开始条件</h3></header><div><span><b>预期交付</b>{currentRoute.deliverables.map(researchRouteUserText).filter(Boolean).map((value) => <small key={value}>{value}</small>)}</span><span><b>{userGap ? "开始前需要" : "所需资源"}</b>{userGap ? <small>{userGap}</small> : currentRoute.requiredResources.map(researchRouteUserText).filter(Boolean).map((value) => <small key={value}>{value}</small>)}</span><span><b>核验点</b>{currentRoute.reviewCheckpoints.map(researchRouteUserText).filter(Boolean).map((value) => <small key={value}>{value}</small>)}</span></div></section>
      </div>
      <footer className="research-route-detail-actions"><button type="button" disabled={busy} data-dialog-dismiss onClick={close}>返回路线列表</button><button type="button" className="primary" disabled={busy || !canAdopt} onClick={() => adopt(currentRoute)}><Icon name="play" size={14}/>{busy ? availableNow ? "正在创建科研任务…" : "正在准备方案…" : availableNow ? "采用并开始任务" : canAdopt ? "采用并补充资料" : validation === "checking" ? "正在核验路线…" : "路线不可用"}</button></footer>
    </section>
  </ModalBackdrop>, document.body);
}

const researchActivityStatusText: Record<string, string> = {
  prepared: "已准备", running: "执行中", completed: "已完成", failed: "失败", cancelled: "已取消", interrupted: "已中断",
};

function formatResearchActivityDuration(seconds: number) {
  if (!Number.isFinite(seconds) || seconds <= 0) return "刚刚开始";
  if (seconds < 60) return `${seconds} 秒`;
  return `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`;
}

function approvalIDFromBusy(value: string) {
  const separator = value.indexOf(":");
  if (separator <= 0) return "";
  const kind = value.slice(0, separator);
  return kind === "approval" || kind === "ai-approval" ? value.slice(separator + 1) : "";
}

function WorkflowMessageActivityCard({ activity: entry, busy, resolveApproval, retryStatus }: { activity: WorkflowMessageActivity; retryStatus?: RetryStatus | null; busy: string; resolveApproval?: (approval: Approval, allow: boolean) => void | Promise<void> }) {
  const activity = entry.activity;
  // Activities are already scoped to this AI execution; never combine them
  // with another stage or render a run-wide aggregate card.
  const calls = [...(activity.toolCalls ?? []), ...entry.workflowTools]
    .filter((call, index, values) => values.findIndex((value) => value.id === call.id) === index)
    .sort((left, right) => Date.parse(left.startedAt || left.createdAt) - Date.parse(right.startedAt || right.createdAt));
  const approvals = [...entry.approvals, ...(activity.pendingApprovals ?? [])].filter((approval, index, values) => values.findIndex((item) => item.id === approval.id || item.toolCallId === approval.toolCallId) === index);
  const running = ["prepared", "running"].includes(activity.status) || calls.some((call) => ["pending", "awaiting_approval", "running"].includes(call.status));
  const waiting = approvals.length > 0 || calls.some((call) => call.status === "awaiting_approval");
  const failed = activity.status === "failed" || Boolean(activity.lastError) || calls.some((call) => ["failed", "error", "denied"].includes(call.status) || Boolean(call.errorMessage));
  const statusKind = waiting ? "waiting" : running ? "running" : failed ? "failed" : "completed";
  const statusText = waiting ? "等待授权" : running ? `${entry.stageLabel || "本轮"}处理中` : failed ? `${entry.stageLabel || "本轮"}未完成` : `${entry.stageLabel || "本轮"}已完成`;
  const activeCall = calls.find((call) => ["pending", "awaiting_approval", "running"].includes(call.status));
  const action = normalizeDisplayText((running && retryStatusLabel(retryStatus)) || (activeCall ? (activeCall.status === "awaiting_approval" ? `等待授权：${activeCall.summary || activeCall.toolName}` : `正在执行：${activeCall.summary || activeCall.toolName}`) : activity.currentAction || (running ? "正在处理" : "本轮活动记录")));
  const active = running || waiting;
  const approvalByCall = new Map(approvals.map((approval) => [approval.toolCallId, approval]));
  const detail = <>
      {activity.currentDraft && <p className="research-message-current-draft">{normalizeDisplayText(activity.currentDraft)}</p>}
      {approvals.length > 0 && <div className="research-message-activity-alert"><Icon name="shield" size={13}/><div><span>有 {approvals.length} 个调用等待授权</span>{approvals.map((approval) => <div className="research-message-approval" key={approval.id}><span><b>{normalizeDisplayText(approval.toolName)}</b>{approval.resource && ` · ${normalizeDisplayText(approval.resource)}`}</span><nav><button type="button" disabled={Boolean(busy)} onClick={() => resolveApproval?.(approval, false)}>拒绝</button><button type="button" className="accept" disabled={Boolean(busy)} onClick={() => resolveApproval?.(approval, true)}>{busy === `ai-approval:${approval.id}` ? "处理中…" : "接受"}</button></nav></div>)}</div></div>}
      {activity.lastError && <p className="research-message-activity-error"><Icon name="close" size={12}/><span>{normalizeDisplayText(activity.lastError)}</span></p>}
      {calls.map((call) => <UnifiedToolActivityCard key={call.id} activity={{ ...call, source: "workflow", approval: approvalByCall.get(call.id), resolvingApprovalId: approvalIDFromBusy(busy), resolveApproval }}/>) }
    </>;
  return <><div className="research-stage-progress">{entry.progressLabel && <p>{entry.progressLabel}</p>}{entry.resultSummary && !running && <p>{entry.resultSummary}</p>}</div><RunProcessFrame active={active} waiting={waiting} failed={failed} statusText={statusText} duration={formatResearchActivityDuration(activity.elapsedSeconds)} statusNode={<b>{statusText} {formatResearchActivityDuration(activity.elapsedSeconds)}</b>} liveDetail={action} resetKey={activity.executionId} className="research-message-activity" stateClassName={`research-message-activity-state ${statusKind}`} detailClassName="research-message-activity-detail" detail={detail}/></>
}

function WorkflowTimelineToolCard({ item }: { item: WorkflowTimelineCall }) {
  return <article className="message assistant research-workflow-tool">
    <div className="avatar"><Icon name="tool" size={17}/></div>
    <div className="message-body">
      <div className="message-meta"><b>{item.label}</b><span>{new Date(item.call.startedAt || item.call.createdAt).toLocaleTimeString()}</span></div>
      <UnifiedToolActivityCard activity={{ ...item.call, source: "workflow", approval: item.approval, resolvingApprovalId: approvalIDFromBusy(item.busy), resolveApproval: item.resolveApproval }}/>
    </div>
  </article>;
}


function observeChatAutoFollow(chat: HTMLElement, following: () => boolean) {
  let frame = 0;
  const follow = () => {
    if (frame || !following()) return;
    frame = window.requestAnimationFrame(() => {
      frame = 0;
      if (following()) chat.scrollTo({ top: chat.scrollHeight, behavior: "instant" });
    });
  };
  const observer = new MutationObserver(follow);
  observer.observe(chat, { childList: true, subtree: true, characterData: true });
  chat.addEventListener("load", follow, true);
  return () => {
    observer.disconnect();
    chat.removeEventListener("load", follow, true);
    if (frame) window.cancelAnimationFrame(frame);
  };
}


function workflowMessageActivities(detail: WorkflowRunDetail, resolveApproval?: (approval: Approval, allow: boolean) => void | Promise<void>): Record<string, WorkflowMessageActivity> {
  const projected = new Map((detail.aiActivities ?? []).map((activity) => [activity.executionId, activity]));
  const result: Record<string, WorkflowMessageActivity> = {};
  for (const execution of detail.aiExecutions) {
    if (!execution.chatRunId) continue;
    const projectedActivity = projected.get(execution.id);
    const activity = projectedActivity && projectedActivity.chatRunId === execution.chatRunId ? projectedActivity : {
      executionId: execution.id, workflowStepId: execution.workflowStepId, chatRunId: execution.chatRunId,
      status: execution.status, modelId: execution.modelId, modelTurns: execution.modelTurns,
      inputTokens: execution.inputTokens, outputTokens: execution.outputTokens, reasoningTokens: execution.reasoningTokens,
      currentAction: execution.status === "prepared" ? "正在准备 AI 阶段" : execution.status === "running" ? "正在处理" : researchActivityStatusText[execution.status] ?? execution.status,
      elapsedSeconds: execution.startedAt ? Math.max(0, Math.floor(((execution.completedAt ? Date.parse(execution.completedAt) : Date.now()) - Date.parse(execution.startedAt)) / 1000)) : 0,
      toolCalls: [], updatedAt: execution.updatedAt, startedAt: execution.startedAt,
    } satisfies AIStageActivity;
    const step = detail.steps.find((value) => value.id === execution.workflowStepId);
    const node = detail.run.compilation.nodes.find((value) => value.id === step?.nodeId);
    const output = workflowRecord(execution.output);
    const assessments = workflowObjectArray(output.candidateAssessments);
    const queries = Array.isArray(output.queries) ? output.queries : [];
    const completed = execution.status === "completed";
    const resultSummary = completed && assessments.length ? `本批筛选 ${assessments.length} 条：优先核验 ${assessments.filter((a) => a.decision === "core").length} 条，待核验 ${assessments.filter((a) => a.decision === "support").length} 条，排除 ${assessments.filter((a) => a.decision === "exclude").length} 条。` : completed && output.phase === "synthesis" ? `已整理本组证据，形成 ${workflowObjectArray(output.findings).length} 项发现、${workflowObjectArray(output.uncertainties).length} 项待核验问题。` : completed && output.phase === "coverage" ? "已完成跨批证据覆盖评估。" : completed && queries.length ? `已生成 ${queries.length} 个互补检索式。` : "";
    let progressLabel = "";
    if (step?.attempt === execution.attempt && !completed) {
      const progress = workflowRecord(step.input._literature);
	  const selectedProgress = workflowRecord(step.input._selectedEvidence);
	  if (selectedProgress.phase === "batch") progressLabel = `正在分析本批 ${Array.isArray(selectedProgress.documents) ? selectedProgress.documents.length : 0} 份入选文档`;
	  if (selectedProgress.phase === "coverage") progressLabel = "正在综合入选文献的发现、冲突与局限";
      if (typeof progress.total === "number" && typeof progress.pending === "number") progressLabel = progress.readback ? "正在回查原始文献材料" : progress.phase === "synthesis" ? `正在进行第 ${progress.level || 1} 层证据综合` : progress.phase === "coverage" ? "正在汇总覆盖情况" : progress.triage ? `轻量初筛：已处理 ${progress.total - progress.pending} / ${progress.total} 条候选` : `正在提取保留文献的详细证据，剩余 ${progress.pending} 条`;
    }
    result[execution.chatRunId] = { activity, stageLabel: node?.name, resultSummary, progressLabel, workflowTools: [], approvals: [], busy: "", resolveApproval };
  }
  const workflowApprovals = detail.pendingApprovals ?? [];
  for (const item of Object.values(result)) {
    item.approvals = [...workflowApprovals.filter((approval) => item.workflowTools.some((call) => call.id === approval.toolCallId)), ...(item.activity.pendingApprovals ?? [])];
  }
  // Host tools are separate chronological entries; AI/Skill tools remain
  // inside their own execution card, without duplicating host actions.
  return result;
}

function ResearchDeliveryStatus({ assessment, runStatus, recoveryVisible = false }: { assessment?: ResearchDeliveryAssessment; runStatus: string; recoveryVisible?: boolean }) {
  if (!assessment || !["completed", "failed", "interrupted"].includes(runStatus) || !["unverified", "blocked", "revision_required"].includes(assessment.status) || recoveryVisible) return null;
  const label = assessment.status === "reviewed" ? "交付检查已通过" : assessment.status === "revision_required" ? "需要返修" : assessment.status === "unverified" ? "交付尚未验证" : assessment.status === "blocked" ? "交付被阻断" : "等待交付验收";
  return <section className="research-interaction-card research-delivery-assessment" aria-label="科研交付验收">
    <header><div><h3>{assessment.label} · {label}</h3><p>{assessment.summary}</p></div></header>
    <ResearchAcceptanceDetails assessment={assessment}/>
  </section>;
}

function ResearchAcceptanceDetails({ assessment }: { assessment?: ResearchDeliveryAssessment }) {
  if (!assessment || (!assessment.checks?.length && !assessment.limitations?.length)) return null;
  return <div className="research-acceptance-details">
    {Boolean(assessment.checks?.length) && <details><summary>逐项验收核对（AI 辅助审查）</summary><ol>{assessment.checks?.map((check) => <li key={check.criterionId}><b>{check.status === "met" ? "已满足" : "未满足"}</b>：{check.basis}</li>)}</ol></details>}
    {Boolean(assessment.limitations?.length) && <details><summary>交付局限</summary><ul>{assessment.limitations?.map((limitation, index) => <li key={index}>{limitation}</li>)}</ul></details>}
  </div>;
}

function WorkflowInteractionCards({ detail, selectedAttachmentIds, setSelectedAttachmentIds, starterPlan, starterPlanChecking, starterPlanError, pendingDecisionStep, note, context, selectedCandidateIds, selectedCitationKeys, acceptLimitedEvidence, confirmRetry, busy, setNote, setContext, setSelectedCandidateIds, setSelectedCitationKeys, setAcceptLimitedEvidence, setConfirmRetry, resolveApproval, decide, retryStep, adoptResearchRoute, answerClarification, replanResearchStarter }: {
  detail: WorkflowRunDetail;
  selectedAttachmentIds: string[];
  setSelectedAttachmentIds: (ids: string[]) => void;
  starterPlan: ResearchStarterPlan | null;
  starterPlanChecking: boolean;
  starterPlanError: string;
  pendingDecisionStep?: WorkflowStep;
  note: string;
  context: string;
  selectedCandidateIds: string[];
  selectedCitationKeys: string[];
  acceptLimitedEvidence: boolean;
  confirmRetry: boolean;
  busy: string;
  setNote: (value: string) => void;
  setContext: (value: string) => void;
  setSelectedCandidateIds: (value: string[]) => void;
  setSelectedCitationKeys: (value: string[]) => void;
  setAcceptLimitedEvidence: (value: boolean) => void;
  setConfirmRetry: (value: boolean) => void;
	resolveApproval: (approval: Approval, allow: boolean) => void;
	decide: (step: WorkflowStep, approved: boolean, continueWithoutCitations?: boolean, acceptLimitedEvidence?: boolean) => void;
	retryStep: (step: WorkflowStep, revisionNodeId?: string, useRecommendation?: boolean, expectedReviewSha256?: string) => void;
	adoptResearchRoute: (route: ResearchStarterRoute) => void;
	answerClarification: (answers: Record<string, string[]>) => void;
  replanResearchStarter: () => void;
}) {
	const [routeDetail, setRouteDetail] = useState<ResearchStarterRoute | null>(null);
	const [clarificationAnswers, setClarificationAnswers] = useState<Record<string, string[]>>({});
	const [clarificationQuestion, setClarificationQuestion] = useState<ResearchClarificationQuestion | null>(null);
  const [clarificationDraft, setClarificationDraft] = useState<string[]>([]);
  const [confirmReplan, setConfirmReplan] = useState(false);
  const retryableStep = detail.steps.find((step) => ["failed", "interrupted", "outcome_unknown"].includes(step.status) && ["failed", "interrupted"].includes(detail.run.status));
  const decisionNode = pendingDecisionStep ? detail.run.compilation.nodes.find((node) => node.id === pendingDecisionStep.nodeId) : undefined;
  const retryNode = retryableStep ? detail.run.compilation.nodes.find((node) => node.id === retryableStep.nodeId) : undefined;
	const reviewGateFailure = Boolean(retryableStep && retryNode?.tool?.qualifiedName === "builtin.research.workflow.review.gate");
	const reviewIssues = retryableStep && reviewGateFailure ? workflowReviewIssues(detail, retryableStep) : null;
  const retryNeedsConfirmation = Boolean(retryNode?.sideEffect);

	const allowEmptyCitations = detail.run.compilation.nodes.some((node) => node.id === "python_analysis" && node.kind === "python" || node.id === "research_design" && ["agent_stage", "ai_analysis"].includes(node.kind));
	const clarification = validResearchClarification(starterPlan?.clarification) ? starterPlan?.clarification : undefined;
	const clarificationKey = `${detail.run.id}:${JSON.stringify(clarification ?? null)}`;
	useEffect(() => {
		setClarificationAnswers({});
    setClarificationDraft([]);
    setConfirmReplan(false);
		setClarificationQuestion(null);
	}, [clarificationKey]);

	const clarificationComplete = Boolean(clarification && clarification.questions.filter((question) => question.required).every((question) => (clarificationAnswers[question.id] ?? []).length > 0));
  return <div className="research-interaction-cards">
    <ResearchDeliveryStatus assessment={detail.deliveryAssessment} runStatus={detail.run.status} recoveryVisible={Boolean(retryableStep)}/>
    {detail.run.workflowPurpose === "research_starter" && detail.run.status === "completed" && <section className="research-interaction-card research-replan-card" aria-label="更新研究路线" aria-busy={busy === "replan-research-starter"}>
      <header><span><Icon name="refresh" size={17}/></span><div><p>路线调整</p><h3>更新研究路线</h3></div><em role="status">{busy === "replan-research-starter" ? "正在重新规划" : starterPlanChecking ? "正在核验" : busy ? "请等待当前操作" : confirmReplan ? "可以开始" : "待确认"}</em></header>
      <div className="research-replan-body"><p>补充了数据，或当前问题需要重新梳理？先将资料上传到当前任务，再让 AI 重新读取并规划。</p><div className="research-replan-notes"><span><Icon name="shield" size={13}/>保留原路线与历史记录</span><span>不会自动提交你的选择</span></div></div>
      <footer><label className="research-replan-consent"><input type="checkbox" checked={confirmReplan} disabled={Boolean(busy) || starterPlanChecking} onChange={(event) => setConfirmReplan(event.target.checked)}/><span><b>确认再次调用 AI 模型</b><small>会消耗模型额度，可能产生费用</small></span></label><button type="button" className="primary" disabled={!confirmReplan || Boolean(busy) || starterPlanChecking} onClick={replanResearchStarter}><Icon name="refresh" size={14}/>{busy === "replan-research-starter" ? "正在重新规划…" : "重新读取资料并规划"}</button></footer>
    </section>}
	{starterPlan && <section className="research-interaction-card research-route-choice">
	  <header><span><Icon name="spark" size={15}/></span><div><p>AI 研究启动已完成</p><h3>{starterPlan.normalizedQuestion}</h3></div><em>{starterPlanChecking ? "正在核验" : starterPlanError ? "路线不可用" : starterPlan.confidence === "high" ? "高置信建议" : starterPlan.confidence === "medium" ? "中等置信建议" : "探索性建议"}</em></header>
	  {starterPlanChecking && <div className="research-route-validation-checking"><Icon name="refresh" size={13}/>正在核对阶段顺序、资源条件和 Skill 快照；核验完成前不能启动路线。</div>}
	  {starterPlanError && <div className="research-route-validation-error"><Icon name="close" size={13}/>宿主未能核验这份路线：{starterPlanError}</div>}
	  <div className="research-route-recommendation"><b>推荐依据</b><p>{starterPlan.recommendationReason}</p></div>
	  {Boolean(starterPlan.selectedSkills?.length) && <div className="research-route-skill-summary"><Icon name="skill" size={14}/><span>{starterPlanChecking || starterPlanError ? "Skill 实际加载记录尚未通过核验" : `已核验实际加载 ${starterPlan.loadedSkills?.length ?? 0} 个科研 Skill`}；方案声明使用 {starterPlan.selectedSkills.length} 个</span></div>}
	  {clarification && <section className="research-clarification"><header><b>{clarification.intro || "开始规划前，请先确认几个关键研究边界"}</b><small>请先确认关键边界，AI 会据此生成路线</small></header>{clarification.questions.map((question) => { const summary = clarificationSummary(question, clarificationAnswers[question.id] ?? []); return <fieldset key={question.id}><legend>{question.text}<em>{clarificationModeLabel(question)}</em>{question.required && <em>必选</em>}</legend><div className="research-clarification-answer"><span className={summary ? "selected" : "empty"}>{summary ? `已选：${summary}` : "尚未选择"}</span><button type="button" disabled={Boolean(busy) || starterPlanChecking || Boolean(starterPlanError)} onClick={() => { setClarificationDraft([...(clarificationAnswers[question.id] ?? [])]); setClarificationQuestion(question); }}>{summary ? "重新选择" : "选择"}</button></div></fieldset>; })}<footer><button type="button" className="primary" disabled={Boolean(busy) || starterPlanChecking || Boolean(starterPlanError) || !clarificationComplete} onClick={() => answerClarification(clarificationAnswers)}><Icon name="spark" size={14}/>{busy === "answer-clarification" ? "正在按选择重新规划…" : "按我的选择生成研究路线"}</button></footer></section>}
	  {!clarification && <div className="research-route-options">{starterPlan.routes.map((route) => { const validation = researchRouteValidation(route); const availableNow = validation === "ready"; const canAdopt = researchRouteCanAdopt(route); const userGap = researchRouteUserGap(route); return <article key={route.routeId} className={route.routeId === starterPlan.recommendedRouteId ? "recommended" : ""}>
		<header><span><b>{researchRouteUserText(route.title)}</b><small>{route.routeId === starterPlan.recommendedRouteId ? "AI 推荐" : "候选路线"}</small></span><em className={validation}>{researchRouteStatusLabel(route)}</em></header>
		<p className="research-route-card-summary">{researchRouteUserText(route.reason)}</p>
		<div className="research-route-card-meta"><span><b>{route.layers.length}</b> 个层次</span><span><b>{route.stageIds.length}</b> 个阶段</span><span><b>{route.deliverables.length}</b> 项交付</span>{userGap && <span className="gap">需要补充资料</span>}</div>
		{userGap && <p className="research-route-card-gap">开始前需要：{userGap}</p>}
        <ResearchRoutePlanningNotes route={route}/>
		<footer className="research-route-card-actions"><button type="button" disabled={Boolean(busy)} onClick={() => setRouteDetail(route)}>查看路线详情</button><button type="button" className={route.routeId === starterPlan.recommendedRouteId ? "primary" : ""} disabled={Boolean(busy) || !canAdopt} onClick={() => adoptResearchRoute(route)}>{busy === `adopt-route:${route.routeId}` ? availableNow ? "正在创建科研任务…" : "正在准备方案…" : availableNow ? "采用并开始任务" : canAdopt ? "采用并补充数据" : validation === "checking" ? "正在核验路线…" : "路线不可用"}</button></footer>
	  </article>; })}</div>}
	  {(starterPlan.missingInformation.length > 0 || starterPlan.limitations.length > 0) && <details><summary>信息缺口与局限</summary><ul>{[...starterPlan.missingInformation, ...starterPlan.limitations].map((value, index) => <li key={`${value}:${index}`}>{value}</li>)}</ul></details>}
	</section>}
	{clarificationQuestion && clarification && !starterPlanChecking && !starterPlanError && createPortal(<ResearchClarificationDialog question={clarificationQuestion} draft={clarificationDraft} setDraft={setClarificationDraft} close={() => setClarificationQuestion(null)} confirm={(selected) => { setClarificationAnswers((current) => ({ ...current, [clarificationQuestion.id]: selected })); setClarificationQuestion(null); }}/>, document.body)}
	{routeDetail && starterPlan && <ResearchRouteDetailDialog route={routeDetail} plan={starterPlan} busy={busy === `adopt-route:${routeDetail.routeId}`} close={() => setRouteDetail(null)} adopt={(selectedRoute) => { setRouteDetail(null); if (researchRouteCanAdopt(selectedRoute)) adoptResearchRoute(selectedRoute); }}/>}
    {pendingDecisionStep && <section className="research-interaction-card decision">
      <header><span><Icon name={pendingDecisionStep.nodeKind === "candidate_selection" ? "search" : "check"} size={15}/></span><div><p>{pendingDecisionStep.nodeKind === "candidate_selection" ? "文献候选" : pendingDecisionStep.nodeKind === "citation_selection" ? "证据选择" : "人工检查点"}</p><h3>{decisionNode?.name ?? "需要你的决定"}</h3></div><em>{workflowStepLabels[pendingDecisionStep.status]}</em></header>
	  <WorkflowHumanDecision projectId={detail.run.projectId} taskId={detail.run.researchTaskId} supportsMaterials={detail.run.compilation.edges.some(edge => edge.fromNode === pendingDecisionStep.nodeId && edge.fromPort === "selectedAttachmentIds")} selectedAttachmentIds={selectedAttachmentIds} setSelectedAttachmentIds={setSelectedAttachmentIds} step={pendingDecisionStep} prompt={decisionNode?.prompt} note={note} context={context} selectedCandidateIds={selectedCandidateIds} selectedCitationKeys={selectedCitationKeys} acceptLimitedEvidence={acceptLimitedEvidence} busy={busy} setNote={setNote} setContext={setContext} setSelectedCandidateIds={setSelectedCandidateIds} setSelectedCitationKeys={setSelectedCitationKeys} setAcceptLimitedEvidence={setAcceptLimitedEvidence} allowEmptyCitations={allowEmptyCitations} decide={(approved, continueWithoutCitations, acceptLimited) => decide(pendingDecisionStep, approved, continueWithoutCitations, acceptLimited)}/>
    </section>}
    {retryableStep && <section className="research-interaction-card error">
      <header><span><Icon name="refresh" size={15}/></span><div><p>{reviewGateFailure ? "报告需要修改" : "执行需要处理"}</p><h3>{retryNode?.name ?? `阶段 ${retryableStep.ordinal + 1}`}</h3></div><em>{workflowStepLabels[retryableStep.status]}</em></header>
      {reviewGateFailure ? <ResearchReviewFeedback issues={reviewIssues} errorCode={retryableStep.errorCode || detail.run.errorCode} errorMessage={retryableStep.errorMessage || detail.run.errorMessage}/> : <div className="research-interaction-copy"><code>{retryableStep.errorCode || detail.run.errorCode || "WORKFLOW_STEP_FAILED"}</code><p>{retryableStep.errorMessage || detail.run.errorMessage || "当前阶段未能完成。已完成的阶段和产物不会被删除。"}</p></div>}
      {reviewGateFailure && <div className="research-review-recovery"><b>接下来会发生什么</b><p>保留你的研究目标和原始资料，从选定步骤开始修改，随后重新检查结果。旧版本和处理记录仍会保留，检查通过前不会交付。</p></div>}
      {reviewGateFailure ? <ResearchRevisionControls resetKey={`${detail.run.id}:${retryableStep.id}:${retryableStep.attempt}`} targets={detail.reviewRevisionTargets ?? []} recommendation={detail.reviewRevisionRecommendation} busy={Boolean(busy)} confirmed={confirmRetry} setConfirmed={setConfirmRetry} submit={(nodeId, recommended, reviewSha) => retryStep(retryableStep, nodeId, recommended, reviewSha)}/> : <footer className="research-retry-actions">{retryNeedsConfirmation && <label><input type="checkbox" checked={confirmRetry} onChange={(event) => setConfirmRetry(event.target.checked)}/>确认该动作可能已经产生副作用</label>}<button type="button" className="primary" onClick={() => retryStep(retryableStep)} disabled={Boolean(busy) || Boolean(retryNeedsConfirmation && !confirmRetry)}><Icon name="refresh" size={14}/><span>{busy === `retry:${retryableStep.id}` ? "正在重试…" : "重试当前阶段"}</span></button></footer>}
    </section>}
  </div>;
}


const researchTimelineLabels: Record<string, string> = {
  "workflow.created": "开始研究", "workflow.completed": "本轮流程已完成", "workflow.human_confirmation_requested": "人工检查点",
  "workflow.agent_stage_review_requested": "确认阶段结果", "workflow.human_decided": "已提交决定", "workflow.failed": "本次执行失败",
  "workflow.interrupted": "执行已中断", "workflow.outcome_unknown": "执行结果待确认", "workflow.paused": "任务已暂停", "workflow.resumed": "任务已继续",
  "workflow.cancel_requested": "已取消任务", "workflow.review_revision_queued": "按审查意见返修", "workflow.user_revision_queued": "已确认返修方案",
  "workflow.upstream_revision_queued": "返回方法实现修正", "workflow.step_retry_queued": "已安排重试", "research.route_adopted": "已采纳研究路线",
};

function ResearchTimelineHistory({ entry }: {entry: ResearchTimelineEntry}) {
  // Failures still own their current recovery controls and tool audit. Avoid
  // repeating processed failure/retry notifications as separate history cards.
  if (entry.kind === "event" && (entry.eventType === "workflow.failed" || entry.eventType === "workflow.step_retry_queued")) return null;
  const snapshot = entry.snapshot;
  if (entry.eventType === "workflow.local_evidence_requested") return <section className="research-timeline-history" aria-label="补查所选文献"><p>正在所选文献内补查关键原文，更新引用后重新综合；不会新增文献或联网。</p></section>;
  if (entry.eventType === "workflow.materials_reused") return <section className="research-timeline-history" aria-label="沿用已选材料"><p>已沿用课题输入时选择的文献，无需重复确认。</p></section>;
  if (entry.eventType === "workflow.evidence_refreshed") return <section className="research-timeline-history" aria-label="补充全文已保存">
    <p>补充全文已保存，正在更新引用并重新综合；不会重新检索或初筛文献。</p>
  </section>;
  const outputs = snapshot.outputs ?? {};
  const plan = outputs.plan as Partial<ResearchStarterPlan> | undefined;
  const report = outputs.report_draft as Partial<WorkflowResearchReport> | undefined;
  const design = outputs.research_design as Partial<WorkflowResearchDesign> | undefined;
  const inputs = snapshot.inputs ?? {};
  const starter = inputs.starter_context as {researchIdea?: string; priorStarterRunId?: string; clarificationAnswers?: Record<string, string[]>} | undefined;
  if (entry.kind === "event" && entry.eventType === "workflow.created" && !starter?.priorStarterRunId) return null;
  const isPlanner = snapshot.workflowPurpose === "research_starter";
  const title = entry.kind === "registration" ? "已登记为科研产物"
    : entry.eventType === "workflow.completed" && isPlanner ? "研究路线规划记录"
    : entry.eventType === "workflow.completed" && (report || design) ? "本轮交付记录"
    : entry.eventType === "workflow.created" && starter?.priorStarterRunId ? starter.clarificationAnswers ? "已确认研究需求，继续规划" : "重新规划研究路线"
    : researchTimelineLabels[entry.eventType ?? ""] ?? "研究活动";
  return <section className="research-timeline-history" aria-label={title}>
    <header><b>{title}</b><time dateTime={entry.createdAt}>{new Date(entry.createdAt).toLocaleString()}</time></header>
    {snapshot.nodeName && <p>{snapshot.nodeName}</p>}
    {entry.eventType === "workflow.created" && <p>{starter?.researchIdea || snapshot.workflowName}</p>}
    {entry.eventType === "research.route_adopted" && <p>{String(snapshot.event?.routeId ?? "")}{snapshot.event?.availableNow === false ? " · 等待补充数据" : " · 开始执行"}</p>}
    {entry.kind === "registration" && <p>{snapshot.name}</p>}
    {plan && <><p>{plan.normalizedQuestion}</p><p>{plan.recommendationReason}</p>{plan.clarification?.questions?.map(question => <details key={question.id}><summary>{question.text}</summary><ul>{question.options.map(option => <li key={option.id}>{option.label}</li>)}</ul></details>)}{plan.routes?.map(route => <details key={route.routeId}><summary>{route.title}</summary><p>{route.reason}</p><ResearchTimelineJSON value={route} label="完整路线"/></details>)}</>}
    {report?.markdown && <CitedMarkdown key={entry.id} text={report.markdown} citations={reportCitationSnapshot(outputs)}/>}
    {design && <><h3>{design.title}</h3><p>{design.researchQuestion}</p><ResearchTimelineJSON value={design} label="研究设计内容"/></>}
    {(snapshot.errorMessage || entry.step?.errorMessage) && <p className="research-timeline-error">{entry.step?.errorMessage || snapshot.errorMessage}</p>}
    {snapshot.prompt && <p>{snapshot.prompt}</p>}
    {snapshot.decision && Object.keys(snapshot.decision).length > 0 && <><p>{snapshot.decision.approved ? "已通过" : "未通过"}{snapshot.decision.note ? `：${snapshot.decision.note}` : ""}</p><ResearchTimelineJSON value={snapshot.decision.context} label="已提交的选择"/></>}
    {entry.step && <ResearchTimelineJSON value={{input: entry.step.input, output: entry.step.output}} label="当时的阶段内容"/>}
    {starter?.clarificationAnswers && <ResearchTimelineJSON value={starter.clarificationAnswers} label="已提交的研究需求"/>}
    {!plan && !report && !design && entry.eventType === "workflow.completed" && Object.keys(outputs).length > 0 && <ResearchTimelineJSON value={outputs} label="本轮输出"/>}
    {snapshot.historical && !Object.keys(outputs).length && entry.eventType === "workflow.completed" && <p>此历史记录没有保留当时的交付快照。</p>}
  </section>;
}

function ResearchTimelineJSON({ value, label }: {value: unknown; label: string}) {
  const [open, setOpen] = useState(false);
  const text = useMemo(() => open ? JSON.stringify(value, null, 2) : "", [open, value]);
  if (!value || typeof value === "object" && !Object.keys(value).length) return null;
  return <details className="research-timeline-json" onToggle={event => setOpen(event.currentTarget.open)}><summary>{label}</summary>{open && <pre>{text}</pre>}</details>;
}

function WorkflowResearchDesignCard({ assessment, design, registered, busy, taskId, saveDeliverable, openArtifacts }: { assessment?: ResearchDeliveryAssessment; design: WorkflowResearchDesign; registered: boolean; taskId?: string; busy: string; saveDeliverable: () => void; openArtifacts: (taskId?: string) => void }) {
  return <section className="research-deliverable-card">
    <header><span><Icon name="check" size={17}/></span><div><p>最终交付</p><h2>研究设计已完成并通过独立审查</h2><small>这是可执行研究方案，不是数据采集、模型训练或统计分析后的实证结论。</small></div><em>审查通过</em></header>
    <div className="research-deliverable-body">
      <ResearchAcceptanceDetails assessment={assessment}/>
      <h1>{design.title}</h1>
      <section className="research-deliverable-question"><b>研究问题</b><p>{design.researchQuestion}</p></section>
      <div className="research-deliverable-summary"><span><b>{design.objectives.length}</b><small>研究目标</small></span><span><b>{design.hypotheses.length}</b><small>研究假设</small></span><span><b>{design.milestones.length}</b><small>实施里程碑</small></span></div>
      <div className="research-deliverable-sections">{workflowResearchDesignSections(design).filter(([, values]) => values.length > 0).map(([title, values], index) => <details key={title} open={index < 2}><summary><span>{title}</span><em>{values.length} 项</em></summary><ol>{values.map((value, itemIndex) => <li key={`${title}:${itemIndex}`}>{value}</li>)}</ol></details>)}</div>
    </div>
    <footer><span><Icon name="shield" size={13}/>研究内容、引用和审查快照已冻结</span>{registered ? <button type="button" className="primary" onClick={() => openArtifacts(taskId)}><Icon name="check" size={13}/>已登记 · 查看科研产物</button> : <button type="button" className="primary" disabled={Boolean(busy)} onClick={saveDeliverable}><Icon name="archive" size={13}/>{busy === "save-deliverable" ? "正在登记…" : "登记为科研产物"}</button>}</footer>
  </section>;
}

function WorkflowResearchReportCard({ assessment, deliveryLabel, report, citations, registered, artifactCount, busy, taskId, saveDeliverable, openArtifacts }: { assessment?: ResearchDeliveryAssessment; deliveryLabel: string; report: WorkflowResearchReport; citations: unknown; registered: boolean; artifactCount: number; taskId?: string; busy: string; saveDeliverable: () => void; openArtifacts: (taskId?: string) => void }) {
  const confidence = report.confidence === "high" ? "高" : report.confidence === "medium" ? "中" : "低";
  return <section className="research-deliverable-card research-report-card">
    <header><span><Icon name="check" size={17}/></span><div><p>最终交付</p><h2>{deliveryLabel}已通过独立审查</h2><small>正文与本次审查快照一致；设计或综述不会仅因生成报告就升级为实证结论。</small></div><em>置信度 {confidence}</em></header>
    <div className="research-deliverable-body">
      <ResearchAcceptanceDetails assessment={assessment}/>
      <section className="research-report-method"><b>方法摘要</b><p>{report.methodSummary}</p></section>
      <div className="research-deliverable-summary"><span><b>{report.claimSummary.length}</b><small>主要结论</small></span><span><b>{report.limitations.length}</b><small>已披露局限</small></span><span><b>{confidence}</b><small>交付置信度</small></span></div>
      <details className="research-report-document" open><summary>查看完整研究报告</summary><CitedMarkdown text={report.markdown} citations={citations}/></details>
    </div>
    <footer><span><Icon name="shield" size={13}/>分析结果、报告与独立审查快照已冻结</span><div>{artifactCount > 0 && <button type="button" onClick={() => openArtifacts(taskId)}><Icon name="archive" size={13}/>查看已有产物</button>}{registered ? <button type="button" className="primary" onClick={() => openArtifacts(taskId)}><Icon name="check" size={13}/>已登记 · 查看报告</button> : <button type="button" className="primary" disabled={Boolean(busy)} onClick={saveDeliverable}><Icon name="archive" size={13}/>{busy === "save-deliverable" ? "正在登记…" : "登记报告为科研产物"}</button>}</div></footer>
  </section>;
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

function workflowTaskQuestion(run: WorkflowRun): { label: string; text: string } | null {
	if (run.workflowPurpose === "research_starter") {
		const starter = run.inputs?.starter_context;
		if (starter && typeof starter === "object" && !Array.isArray(starter)) {
			const idea = (starter as { researchIdea?: unknown }).researchIdea;
			if (typeof idea === "string" && idea.trim()) return { label: "原始研究想法", text: idea.trim() };
		}
	}
  const ports = run.compilation.inputs ?? [];
  const priority = (port: WorkflowPort) => {
    if (port.control === "analysis_request") return 0;
    if (["query", "research_question", "question", "topic"].includes(port.name.toLowerCase())) return 1;
    if (/研究问题|检索式|研究目标|研究主题/u.test(port.description ?? "")) return 2;
    return 3;
  };
  for (const port of [...ports].sort((left, right) => priority(left) - priority(right))) {
    if (port.fileKind) continue;
    const value = run.inputs?.[port.name];
    if (port.control === "analysis_request" && value && typeof value === "object" && !Array.isArray(value)) {
      const goal = (value as { goal?: unknown }).goal;
      if (typeof goal === "string" && goal.trim()) return { label: "研究目标", text: goal.trim() };
      continue;
    }
    if (typeof value !== "string" || !value.trim() || priority(port) > 2) continue;
    return { label: port.description?.trim() || "研究问题或检索式", text: value.trim() };
  }
  return null;
}

function workflowTaskTitle(run: WorkflowRun) {
	const question = workflowTaskQuestion(run)?.text.replace(/\s+/gu, " ").trim();
	if (!question) return run.workflowName || "科研任务";
	const characters = [...question];
	return characters.length > 30 ? `${characters.slice(0, 30).join("")}…` : question;
}

function workflowInputDraft(definition: WorkflowDefinition, initial: Record<string, unknown> = {}): Record<string, string> {
	return Object.fromEntries(definition.inputs.map((port) => {
		const value = initial[port.name];
		return [port.name, value === undefined ? workflowDefaultInput(port) : typeof value === "string" ? value : workflowJSON(value)];
	}));
}

function researchStarterPlan(detail: WorkflowRunDetail): ResearchStarterPlan | null {
	if (detail.run.workflowPurpose !== "research_starter" || detail.run.status !== "completed") return null;
	const value = detail.run.outputs?.plan;
	if (!value || typeof value !== "object" || Array.isArray(value)) return null;
	const plan = value as Partial<ResearchStarterPlan>;
	if (!Array.isArray(plan.selectedSkills) || !Array.isArray(plan.routes) || typeof plan.recommendedRouteId !== "string") return null;
	// New planner output is semantic: it contains stagePlans and deliberately
	// leaves executable stageIds/layers to the host compiler. Keep accepting the
	// legacy presentation fields for historical Runs, but normalize semantic
	// routes with empty host-derived fields so the validation effect can fetch
	// and replace them with the canonical projection.
	const routes = plan.routes.map((route, routeIndex) => {
		if (!route || typeof route !== "object") return null;
		const candidate = route as Partial<ResearchStarterRoute> & { stagePlans?: unknown };
		const semantic = Array.isArray(candidate.stagePlans);
		const legacy = Array.isArray(candidate.stageIds) && Array.isArray(candidate.reviewCheckpoints) && Array.isArray(candidate.layers)
			&& candidate.layers.length >= 3 && candidate.layers.length <= 5;
		if (!semantic && !legacy) return null;
		const layers = (Array.isArray(candidate.layers) ? candidate.layers : []).filter((layer): layer is ResearchRouteLayer => Boolean(layer && typeof layer === "object" && !Array.isArray(layer))).map((layer, layerIndex) => ({
			...layer,
			layerId: typeof layer.layerId === "string" ? layer.layerId : `layer-${layerIndex + 1}`,
			title: typeof layer.title === "string" ? layer.title : `研究层次 ${layerIndex + 1}`,
			objective: typeof layer.objective === "string" ? layer.objective : "",
			stages: (Array.isArray(layer.stages) ? layer.stages : []).filter((stage): stage is ResearchRouteStage => Boolean(stage && typeof stage === "object" && !Array.isArray(stage))).map((stage, stageIndex) => ({
				...stage,
				stageId: typeof stage.stageId === "string" ? stage.stageId : `stage-${layerIndex + 1}-${stageIndex + 1}`,
				objective: typeof stage.objective === "string" ? stage.objective : "",
				methods: workflowStringArray(stage.methods),
				skillNames: workflowStringArray(stage.skillNames),
				inputs: workflowStringArray(stage.inputs),
				outputs: workflowStringArray(stage.outputs),
				humanCheckpoint: stage.humanCheckpoint === true,
			})),
		}));
		return {
			...candidate,
			routeId: typeof candidate.routeId === "string" ? candidate.routeId : `route-${routeIndex + 1}`,
			title: typeof candidate.title === "string" ? candidate.title : "研究路线",
			reason: typeof candidate.reason === "string" ? candidate.reason : "",
			availableNow: candidate.availableNow === true,
			requiredResources: workflowStringArray(candidate.requiredResources),
			deliverables: workflowStringArray(candidate.deliverables),
			blockers: workflowStringArray(candidate.blockers),
			planningNotes: workflowStringArray(candidate.planningNotes),
			stageIds: Array.isArray(candidate.stageIds) ? candidate.stageIds : [],
			reviewCheckpoints: Array.isArray(candidate.reviewCheckpoints) ? candidate.reviewCheckpoints : [],
			// layers: Array.isArray(candidate.layers) ? candidate.layers : []
			layers,
		} as ResearchStarterRoute;
	});
	if (routes.some((route) => route === null)) return null;
	const selectedSkills = plan.selectedSkills.map((skill) => ({
		...skill,
		name: typeof skill?.name === "string" ? skill.name : "",
		role: typeof skill?.role === "string" ? skill.role : "",
		stageIds: workflowStringArray(skill?.stageIds),
		limitations: workflowStringArray(skill?.limitations),
	}));
	const clarification = plan.clarification && typeof plan.clarification === "object" && !Array.isArray(plan.clarification) ? {
		...plan.clarification,
		needsUserInput: plan.clarification.needsUserInput === true,
		intro: typeof plan.clarification.intro === "string" ? plan.clarification.intro : "",
		questions: Array.isArray(plan.clarification.questions) ? plan.clarification.questions.filter((question) => question && typeof question === "object" && !Array.isArray(question)).map((question, index) => ({
			...question,
			id: typeof question.id === "string" ? question.id : `clarification-${index + 1}`,
			text: typeof question.text === "string" ? question.text : "",
			required: question.required === true,
			impact: typeof question.impact === "string" ? question.impact : "",
			options: Array.isArray(question.options) ? question.options.filter((option) => option && typeof option === "object" && !Array.isArray(option)).map((option, optionIndex) => ({
				...option,
				id: typeof option.id === "string" ? option.id : `option-${optionIndex + 1}`,
				label: typeof option.label === "string" ? option.label : "",
			})) : [],
		})) : [],
	} : undefined;
	return {
		...plan,
		normalizedQuestion: typeof plan.normalizedQuestion === "string" ? plan.normalizedQuestion : "",
		researchType: typeof plan.researchType === "string" ? plan.researchType : "",
		availableResources: workflowStringArray(plan.availableResources),
		missingInformation: workflowStringArray(plan.missingInformation),
		selectedSkills,
		routes: routes as ResearchStarterRoute[],
		recommendedRouteId: plan.recommendedRouteId,
		recommendationReason: typeof plan.recommendationReason === "string" ? plan.recommendationReason : "",
		confidence: ["low", "medium", "high"].includes(String(plan.confidence)) ? plan.confidence as ResearchStarterPlan["confidence"] : "low",
		limitations: workflowStringArray(plan.limitations),
		clarification,
	};
}

function validResearchClarification(value: ResearchClarification | undefined): value is ResearchClarification {
  return Boolean(value?.needsUserInput && Array.isArray(value.questions) && value.questions.length > 0 && value.questions.every((question) => question && typeof question.id === "string" && typeof question.text === "string" && Array.isArray(question.options) && question.options.length >= 2));
}

function WorkflowInputFields({ definition, values, disabled, compact = false, frozenRoute = false, chooseFile, update }: {
  definition: WorkflowDefinition;
  values: Record<string, string>;
  disabled: boolean;
  compact?: boolean;
  frozenRoute?: boolean;
  chooseFile: (port: WorkflowPort) => void;
  update: (name: string, value: string) => void;
}) {
  return <>{definition.inputs.map((port) => {
    if (frozenRoute && (port.name === "research_goal" || port.name === "route_context")) return null;
    const raw = values[port.name] ?? "";
    if (port.fileKind) {
      const path = workflowInputFilePath(raw);
      if (port.type === "array" && (port.maxItems ?? 1) > 1) {
        let paths: string[] = [];
        try { const parsed = JSON.parse(raw || "[]"); if (Array.isArray(parsed)) paths = parsed.filter((value): value is string => typeof value === "string"); } catch {}
        return <div key={port.name} className="workflow-data-input workflow-multi-input"><header><b>研究数据及配套表格</b><small>{paths.length} / {port.maxItems ?? 16} 份 · CSV / TSV / XLSX</small></header><button type="button" disabled={disabled || paths.length >= (port.maxItems ?? 16)} onClick={() => chooseFile(port)}><Icon name="folder" size={14}/>添加数据文件</button>{paths.map((value, index) => <div key={value}><span className="workflow-file-name" title={value}>{index + 1}. {value.split(/[\\\\/]/).pop()?.replace(/-[a-f0-9]{64}(?=\\.[^.]+$)/i, "")}</span><button type="button" disabled={disabled} aria-label={`移除 ${value}`} onClick={() => update(port.name, JSON.stringify(paths.filter((_, i) => i !== index)))}><Icon name="close" size={12}/></button></div>)}</div>;
      }
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

function workflowNodeMode(node: WorkflowNode): string {
  if (["human_confirmation", "candidate_selection", "citation_selection"].includes(node.kind)) return "需要确认";
  if (node.kind === "agent_stage" || node.kind === "ai_analysis") return "AI 分析";
  if (node.kind === "python") return "Python";
  if (node.kind === "shell") return "本地命令";
  return "自动执行";
}

function WorkflowTemplateDialog({ template, disabled, close, useTemplate }: { template: WorkflowTemplate; disabled: boolean; close: () => void; useTemplate: () => void }) {
  const confirmationCount = template.definition.nodes.filter((node) => ["human_confirmation", "candidate_selection", "citation_selection"].includes(node.kind)).length;
  const hasPython = template.definition.nodes.some((node) => node.kind === "python");

  return <ModalBackdrop className="modal-backdrop research-template-backdrop" close={close} busy={disabled}>
    <section className="model-modal research-template-dialog" role="dialog" aria-modal="true" aria-labelledby="research-template-title">
      <header><div><span className="dialog-icon"><Icon name="library" size={19}/></span><div><p>方案模板</p><h2 id="research-template-title">{template.name}</h2></div></div><button type="button" className="close" disabled={disabled} data-dialog-dismiss onClick={close} aria-label="关闭模板详情"><Icon name="close"/></button></header>
      <div className="research-template-dialog-body">
        <section className="research-template-summary"><p>{template.description || "按固定研究阶段推进，并保留过程与结果记录。"}</p><div><article><b>{template.definition.nodes.length}</b><small>研究阶段</small></article><article><b>{confirmationCount}</b><small>人工确认</small></article><article><b>{hasPython ? "需要" : "无需"}</b><small>Python 环境</small></article></div></section>
        <section className="research-template-route"><header><h3>执行路线</h3><span>按顺序推进</span></header><ol>{template.definition.nodes.map((node) => <li key={node.id}><i>{template.definition.nodes.indexOf(node) + 1}</i><span><b>{node.name}</b><small>{workflowNodeMode(node)}</small></span></li>)}</ol></section>
        <section className="research-template-deliverables"><h3>预期产物</h3><div>{template.definition.outputs.length ? template.definition.outputs.map((output) => <span key={output.name}><Icon name="check" size={12}/>{output.description || output.name}</span>) : <span><Icon name="check" size={12}/>阶段结果与运行记录</span>}</div></section>
      </div>
      <footer className="research-template-dialog-actions"><button type="button" disabled={disabled} data-dialog-dismiss onClick={close}>返回</button><button type="button" className="primary" disabled={disabled} onClick={useTemplate}><Icon name="play" size={14}/>使用此模板</button></footer>
    </section>
  </ModalBackdrop>;
}

function WorkflowStudio({ project, initialConversationId, modelProfileId, modelId, reasoningLevel, pythonDialogOpen, openPython, openArtifacts, selectConversation, publishStageTasks, publishResearchActivities, publishTaskTimeline, publishComposerLocked, publishRevisionConversationId }: { project: Project; initialConversationId: string; modelProfileId: string; modelId: string; reasoningLevel: ReasoningLevel; pythonDialogOpen: boolean; openPython: () => void; openArtifacts: (taskId?: string) => void; selectConversation: (conversationId: string) => Promise<void>; publishStageTasks: (tasks: Record<string, ResearchStageTask>) => void; publishResearchActivities: (activities: Record<string, WorkflowMessageActivity>) => void; publishTaskTimeline: (value: ResearchTimelineView | null) => void; publishComposerLocked: (locked: boolean) => void; publishRevisionConversationId: (conversationId: string) => void }) {
  const [workflowTemplates, setWorkflowTemplates] = useState<WorkflowTemplate[]>([]);
  const [templateDetail, setTemplateDetail] = useState<WorkflowTemplate | null>(null);
  const [selectedTemplateId, setSelectedTemplateId] = useState("");
  const [selectedTemplateDefinition, setSelectedTemplateDefinition] = useState<WorkflowDefinition | null>(null);
  const [workflows, setWorkflows] = useState<ResearchWorkflow[]>([]);
  const [planSelection, setPlanSelection] = useState<string[]>([]);
  const [selectingPlans, setSelectingPlans] = useState(false);
  useEffect(()=>{setPlanSelection([]);setSelectingPlans(false);},[project.id]);
  const [detail, setDetail] = useState<WorkflowDetail | null>(null);
  const [selectedId, setSelectedId] = useState("");
  const [selectedVersionId, setSelectedVersionId] = useState("");
  const [editor, setEditor] = useState(() => JSON.stringify(emptyWorkflowTemplate.definition, null, 2));
  const [preview, setPreview] = useState<WorkflowPreview | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [feedback, setFeedback] = useState("");
  const [sceneTransition, setSceneTransition] = useState<"" | "adopting" | "adopted">("");
  const [view, setView] = useState<"guide" | "advanced">("guide");
  const [sideView, setSideView] = useState<ResearchSideView>("loading");
  const [projectRuns, setProjectRuns] = useState<WorkflowRun[]>([]);
  const [projectRunsLoading, setProjectRunsLoading] = useState(true);
  const [runDetail, setRunDetail] = useState<WorkflowRunDetail | null>(null);
  const [runInputs, setRunInputs] = useState<Record<string, string>>({});
	const [pendingAdoptedRoute, setPendingAdoptedRoute] = useState<PendingAdoptedRoute | null>(null);
	const [researchIdea, setResearchIdea] = useState("");
	const [referenceAttachmentIds, setReferenceAttachmentIds] = useState<string[]>([]);
	const [referenceBusy, setReferenceBusy] = useState(false);
	const [selectedAttachmentIds, setSelectedAttachmentIds] = useState<string[]>([]);
	useEffect(() => { setReferenceAttachmentIds([]); setSelectedAttachmentIds([]); }, [project.id]);
  const [starterPlan, setStarterPlan] = useState<ResearchStarterPlan | null>(null);
  const [starterPlanChecking, setStarterPlanChecking] = useState(false);
  const [starterPlanError, setStarterPlanError] = useState("");
  const [decisionNote, setDecisionNote] = useState("");
  const [decisionContext, setDecisionContext] = useState("{}");
  const [selectedCandidateIds, setSelectedCandidateIds] = useState<string[]>([]);
  const [selectedCitationKeys, setSelectedCitationKeys] = useState<string[]>([]);
  const [acceptLimitedEvidence, setAcceptLimitedEvidence] = useState(false);
  const [confirmRetry, setConfirmRetry] = useState(false);
  const [runPermissionMode, setRunPermissionMode] = useState<PermissionMode>("full_access");
  const [pythonPreflight, setPythonPreflight] = useState<"checking" | "ready" | "available" | "missing" | "error">("checking");
  const editorTouched = useRef(false);
  const guideRef = useRef<HTMLDivElement>(null);
  const startPanelRef = useRef<HTMLElement>(null);
  const planIdeaRef = useRef<HTMLElement>(null);
  const planTemplateRef = useRef<HTMLElement>(null);
  const savedPlanRef = useRef<HTMLElement>(null);
  const sceneTimerRef = useRef<number | null>(null);
  const activeWorkflowIdRef = useRef("");
  const activeRunIdRef = useRef("");
  const mutationRef = useRef("");
  const pythonRequestRef = useRef(0);
  const restoreConversationRef = useRef("");
  const listRequestRef = useRef(0);
  const workflowRequestRef = useRef(0);
  const projectRunsRequestRef = useRef(0);
  const sideViewResolvedRef = useRef(false);
  const sideViewTouchedRef = useRef(false);
  const runRequestRef = useRef(0);
  const runLoadsInFlightRef = useRef(new Set<number>());
  const starterPlanRequestRef = useRef(0);
  const pendingDecisionStep = runDetail?.steps.find((step) => step.status === "waiting_human_confirmation");
  const currentVersion = detail?.versions.find((value) => value.id === (pendingAdoptedRoute?.workflowVersionId ?? detail.workflow.currentVersionId)) ?? detail?.versions[0];
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
  const visibleWorkflows = useMemo(() => {
	const seen = new Set<string>();
	return workflows.filter((value) => {
	  const key = value.currentDefinitionSha256 || `workflow:${value.id}`;
	  if (seen.has(key)) return false;
	  seen.add(key);
	  return true;
	});
  }, [workflows]);
	const activeProjectRuns = useMemo(() => projectRuns.filter((run) => !workflowTerminal(run.status)), [projectRuns]);

  useEffect(() => () => { if (sceneTimerRef.current !== null) window.clearTimeout(sceneTimerRef.current); }, []);

  useEffect(() => {
    const nodeByStep = new Map((runDetail?.steps ?? []).map((step) => [step.id, runDetail?.run.compilation.nodes.find((node) => node.id === step.nodeId)]));
    publishStageTasks(Object.fromEntries((runDetail?.aiExecutions ?? []).filter((execution) => Boolean(execution.chatRunId)).map((execution) => [execution.chatRunId!, {
      chatRunId: execution.chatRunId!,
      nodeName: nodeByStep.get(execution.workflowStepId)?.name ?? "AI 科研阶段",
      nodeKind: execution.nodeKind,
      promptVersion: execution.promptVersion,
      status: execution.status,
      skillRouting: Boolean(nodeByStep.get(execution.workflowStepId)?.skillRouting),
      reviewPolicy: nodeByStep.get(execution.workflowStepId)?.reviewPolicy === "auto" ? "auto" : "human",
      workflowStatus: runDetail?.run.status,
    }])));
    if (!runDetail) {
      publishResearchActivities({});
      return () => publishStageTasks({});
    }
    const detailSnapshot = runDetail;
    const resolveResearchApproval = (approval: Approval, allow: boolean) => approval.runId === detailSnapshot.run.id
      ? resolveWorkflowApproval(approval, allow)
      : resolveAIApproval(approval, allow);
    const activityMap = workflowMessageActivities(detailSnapshot, resolveResearchApproval);
    for (const item of Object.values(activityMap)) item.busy = busy;
    publishResearchActivities(activityMap);
    return () => { publishStageTasks({}); publishResearchActivities({}); };
  }, [busy, publishResearchActivities, publishStageTasks, runDetail]);

  useEffect(() => {
    const reviewConversation = runDetail?.run.status === "waiting_human_confirmation" && pendingDecisionStep?.nodeKind === "agent_stage";
    publishComposerLocked(Boolean(pendingAdoptedRoute || runDetail && !workflowTerminal(runDetail.run.status) && !reviewConversation));
  }, [pendingDecisionStep?.nodeKind, pendingAdoptedRoute, publishComposerLocked, runDetail]);

  useEffect(() => () => publishComposerLocked(false), [publishComposerLocked]);


  useEffect(() => {
    publishRevisionConversationId(workflowRevisionConversationId(runDetail));
    return () => publishRevisionConversationId("");
  }, [publishRevisionConversationId, runDetail]);

  useEffect(() => {
    setDecisionNote("");
    setDecisionContext("{}");
    setAcceptLimitedEvidence(false);
    const initialMaterials = pendingDecisionStep?.input.referenceMaterials;
    setSelectedAttachmentIds(Array.isArray(initialMaterials) ? initialMaterials.map(v => (v as AttachmentReference).attachmentId).filter((v): v is string => typeof v === "string") : []);
    if (pendingDecisionStep?.nodeKind === "candidate_selection") {
      const screening = workflowCandidateScreening(pendingDecisionStep);
      const offered = new Set(workflowCandidates(pendingDecisionStep).map((value) => value.id));
      setSelectedCandidateIds((screening?.recommendedCandidateIds ?? []).filter((id) => offered.has(id)));
      setSelectedCitationKeys([]);
      return;
    }
    if (pendingDecisionStep?.nodeKind === "citation_selection") {
      const screening = workflowEvidenceScreening(pendingDecisionStep);
      const recommended = new Set(screening?.recommendedReferences ?? []);
      setSelectedCandidateIds([]);
      setSelectedCitationKeys(workflowCitations(pendingDecisionStep).filter((value) => recommended.has(value.reference ?? "")).map((value) => JSON.stringify(value)));
      return;
    }
    setSelectedCandidateIds([]);
    setSelectedCitationKeys([]);
  }, [pendingDecisionStep?.id, pendingDecisionStep?.nodeKind, pendingDecisionStep?.attempt]);

  // A planner Run contains untrusted model output.  Always render a temporary
  // checking projection and replace it with the host-validated projection
  // before allowing route adoption.  This keeps the visible button state and
  // the backend adoption gate on the same source of truth.
  useEffect(() => {
    const current = runDetail;
    if (!current || current.run.workflowPurpose !== "research_starter" || current.run.status !== "completed") {
      ++starterPlanRequestRef.current;
      setStarterPlan(null);
      setStarterPlanChecking(false);
      setStarterPlanError("");
      return;
    }
    const raw = researchStarterPlan(current);
    if (!raw) {
      ++starterPlanRequestRef.current;
      setStarterPlan(null);
      setStarterPlanChecking(false);
      setStarterPlanError("研究启动 Run 没有可展示的候选路线。");
      return;
    }
    const checkingPlan: ResearchStarterPlan = {
      ...raw,
      routes: raw.routes.map((route) => ({ ...route, validation: "checking", validationError: "" })),
    };
    const request = ++starterPlanRequestRef.current;
    let active = true;
    setStarterPlan(checkingPlan);
    setStarterPlanChecking(true);
    setStarterPlanError("");
    void backend<ResearchStarterPlan>("WorkflowFacade", "GetResearchStarterPlan", project.id, current.run.id).then((validated) => {
      if (!active || request !== starterPlanRequestRef.current) return;
      setStarterPlan(validated);
      setStarterPlanChecking(false);
    }).catch((error: unknown) => {
      if (!active || request !== starterPlanRequestRef.current) return;
      const message = errorText(error);
      setStarterPlan({
        ...checkingPlan,
        routes: checkingPlan.routes.map((route) => ({
          ...route,
          validation: "invalid",
          validationError: message,
          blockers: [...route.blockers, message].filter((value, index, values) => values.indexOf(value) === index),
        })),
      });
      setStarterPlanError(message);
      setStarterPlanChecking(false);
    });
    return () => { active = false; };
  }, [project.id, runDetail?.run.id, runDetail?.run.status]);


  const parseDefinition = useCallback((): WorkflowDefinition => {
    if (selectedTemplateDefinition) return selectedTemplateDefinition;
    const parsed = JSON.parse(editor) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("Workflow 定义必须是 JSON 对象。");
    return parsed as WorkflowDefinition;
  }, [editor, selectedTemplateDefinition]);

  const chooseSideView = useCallback((value: ResearchSideViewChoice) => {
	sideViewTouchedRef.current = true;
	sideViewResolvedRef.current = true;
	researchSideViewSession.set(project.id, value);
	setSideView(value);
  }, [project.id]);

  const loadList = useCallback(async (nextSelection?: string) => {
	const request = ++listRequestRef.current;
    setLoading(true);
    try {
      const values = normalizeWorkflowList(await backend<ResearchWorkflow[]>("WorkflowFacade", "List", project.id));
	  if (request !== listRequestRef.current) return;
      setWorkflows(values);
	  const target = nextSelection ?? activeWorkflowIdRef.current;
      if (target && !values.some((value) => value.id === target)) {
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

  const loadProjectRuns = useCallback(async () => {
	const request = ++projectRunsRequestRef.current;
	setProjectRunsLoading(true);
	try {
	  const values = normalizeWorkflowRunList(await backend<WorkflowRun[]>("WorkflowFacade", "ListProjectRuns", project.id, 200));
	  if (request !== projectRunsRequestRef.current) return;
	  const loaded = values;
	  setProjectRuns(loaded);
	  if (!sideViewResolvedRef.current && !sideViewTouchedRef.current) {
		const remembered = researchSideViewSession.get(project.id);
		const target = remembered === "plans" || remembered === "tasks" && loaded.length > 0 ? remembered : loaded.length > 0 ? "tasks" : "plans";
		setSideView(target);
		sideViewResolvedRef.current = true;
	  }
	} catch (error) {
	  if (request === projectRunsRequestRef.current) {
		setFeedback(errorText(error));
		if (!sideViewResolvedRef.current && !sideViewTouchedRef.current) {
		  setSideView("tasks");
		  sideViewResolvedRef.current = true;
		}
	  }
	} finally {
	  if (request === projectRunsRequestRef.current) setProjectRunsLoading(false);
	}
  }, [project.id]);

  useEffect(() => { void loadProjectRuns(); }, [loadProjectRuns]);

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
      const loaded = normalizeWorkflowTemplateList(values);
      setWorkflowTemplates(loaded);
    }).catch((error) => {
      if (active) setFeedback(`参考模板加载失败：${errorText(error)}`);
    });
    return () => { active = false; };
  }, [project.id]);

  const loadRun = useCallback(async (runId: string, quiet = false) => {
	if (!runId) return;
	if (quiet && activeRunIdRef.current !== runId) return;
	// A background poll must never supersede a manual refresh or another poll.
	if (quiet && (runLoadsInFlightRef.current.size > 0 || Boolean(mutationRef.current))) return;
	if (!quiet) activeRunIdRef.current = runId;
	const workflowId = activeWorkflowIdRef.current;
	const request = ++runRequestRef.current;
	runLoadsInFlightRef.current.add(request);
	if (!quiet) setBusy("load-run");
	try {
	  const loaded = normalizeWorkflowRunDetail(await backend<WorkflowRunDetail>("WorkflowFacade", "GetRun", project.id, runId));
	  if (request !== runRequestRef.current || !workflowId || activeWorkflowIdRef.current !== workflowId || loaded.run.workflowId !== workflowId) return;
	  setRunDetail(current => shareSnapshot(current, loaded));
	  setProjectRuns((current) => shareSnapshot(current, current.map((value) => value.id === loaded.run.id ? loaded.run : value)));
      if (!quiet && loaded.run.conversationId) await selectConversation(loaded.run.conversationId);
    } catch (error) {
	  if (!quiet && request === runRequestRef.current && activeWorkflowIdRef.current === workflowId) setFeedback(errorText(error));
    } finally {
	  runLoadsInFlightRef.current.delete(request);
	  if (!quiet && request === runRequestRef.current && activeWorkflowIdRef.current === workflowId) setBusy("");
    }
  }, [project.id, selectConversation]);

  useEffect(() => {
    if (!runDetail || workflowTerminal(runDetail.run.status) || runDetail.run.status === "paused") return;
    const timer = window.setInterval(() => { if (!document.hidden) void loadRun(runDetail.run.id, true); }, 1500);
    return () => window.clearInterval(timer);
  }, [runDetail?.run.id, runDetail?.run.status, loadRun]);

  useEffect(() => {
    if (!runDetail || runDetail.run.status !== "completed" || runDetail.run.workflowPurpose === "research_starter") return;
    const runId = runDetail.run.id;
    let disposed = false, inFlight = false;
    const refresh = async () => {
      if (inFlight || mutationRef.current) return;
      inFlight = true;
      try {
        const proposals = await backend<ResearchRevisionProposal[]>("WorkflowFacade", "ListResearchRevisionProposals", project.id, runId);
        if (!disposed && !mutationRef.current) setRunDetail(current => current?.run.id === runId && current.run.status === "completed" ? shareSnapshot(current, { ...current, revisionProposals: proposals ?? [] }) : current);
      } catch { /* Keep the last snapshot; confirmation always revalidates on the host. */ }
      finally { inFlight = false; }
    };
    const timer = window.setInterval(() => { if (!document.hidden) void refresh(); }, 3000);
    return () => { disposed = true; window.clearInterval(timer); };
  }, [project.id, runDetail?.run.id, runDetail?.run.status, runDetail?.run.workflowPurpose]);

  async function selectWorkflow(value: ResearchWorkflow) {
	if (busy || mutationRef.current) return;
	const request = ++workflowRequestRef.current;
	activeWorkflowIdRef.current = value.id;
	++runRequestRef.current;
	activeRunIdRef.current = "";
	editorTouched.current = true;
    setSelectedTemplateId("");
    setSelectedTemplateDefinition(null);
    setSelectedId(value.id);
    setRunDetail(null);
    setPreview(null);
    setFeedback("");
    setView("guide");
	try {
	  const loaded = normalizeWorkflowDetail(await backend<WorkflowDetail>("WorkflowFacade", "Get", project.id, value.id));
	  if (request !== workflowRequestRef.current || activeWorkflowIdRef.current !== value.id) return;
	  setDetail(loaded);
      const current = loaded.versions.find((version) => version.id === loaded.workflow.currentVersionId) ?? loaded.versions[0];
	  if (current) {
		setSelectedVersionId(current.id);
		setEditor(JSON.stringify(current.definition, null, 2));
		setRunInputs(workflowInputDraft(current.definition));
	  }
	} catch (error) {
	  if (request === workflowRequestRef.current && activeWorkflowIdRef.current === value.id) setFeedback(errorText(error));
    }
  }

  async function openProjectRun(run: WorkflowRun) {
	if (busy || mutationRef.current) return;
	chooseSideView("tasks");
	try {
	if (run.workflowPurpose === "research_starter" && run.status === "completed") {
	  const request = ++workflowRequestRef.current;
	  const adopted = await backend<AdoptResearchRouteResult | null>("WorkflowFacade", "GetPendingResearchAdoption", project.id, run.id);
	  if (request !== workflowRequestRef.current) return;
	  if (adopted) {
	    const loaded = normalizeWorkflowDetail(await backend<WorkflowDetail>("WorkflowFacade", "Get", project.id, adopted.workflow.workflow.id));
	    if (request !== workflowRequestRef.current) return;
	    activeWorkflowIdRef.current = loaded.workflow.id; activeRunIdRef.current = ""; ++runRequestRef.current;
	    setDetail(loaded); setRunDetail(null); setSelectedId(loaded.workflow.id); setSelectedTemplateId(""); setSelectedTemplateDefinition(null);
	    setPendingAdoptedRoute({starterRunId: run.id, researchTaskId: adopted.researchTaskId, researchGoal: adopted.researchIdea, routeId: adopted.routeId, workflowId: loaded.workflow.id, workflowVersionId: adopted.workflow.version.id});
	    setRunInputs(workflowInputDraft(adopted.workflow.version.definition, adopted.initialInputs)); setView("guide");
	    if (run.conversationId) await selectConversation(run.conversationId);
	    return;
	  }
	}
	setPendingAdoptedRoute(null);
	const owner = workflows.find((value) => value.id === run.workflowId);
	if (owner && (activeWorkflowIdRef.current !== owner.id || !detail || detail.workflow.id !== owner.id)) await selectWorkflow(owner);
	if (!owner) {
		const request = ++workflowRequestRef.current;
		activeWorkflowIdRef.current = run.workflowId;
		++runRequestRef.current;
		try {
			const loaded = normalizeWorkflowDetail(await backend<WorkflowDetail>("WorkflowFacade", "Get", project.id, run.workflowId));
			if (request !== workflowRequestRef.current || activeWorkflowIdRef.current !== run.workflowId) return;
			setDetail(loaded); setSelectedId(""); setSelectedTemplateId(""); setSelectedTemplateDefinition(null);
			const current = loaded.versions.find((value) => value.id === run.workflowVersionId) ?? loaded.versions[0];
			if (current) { setSelectedVersionId(current.id); setEditor(JSON.stringify(current.definition, null, 2)); setRunInputs(workflowInputDraft(current.definition)); }
		} catch (error) { setFeedback(errorText(error)); return; }
	}
	await loadRun(run.id);
	} catch (error) { setFeedback(errorText(error)); }
  }

  useEffect(() => {
    if (!initialConversationId || loading || runDetail || pendingAdoptedRoute || restoreConversationRef.current === initialConversationId) return;
    restoreConversationRef.current = initialConversationId;
    let active = true;
    void (async () => {
	  const lookup = await backend<WorkflowConversationLookup>("WorkflowFacade", "GetRunByConversation", initialConversationId);
	  const matched = lookup.found && lookup.run?.run.projectId === project.id ? lookup.run.run : undefined;
	  if (!matched || !active) return;
	  if (active) await openProjectRun(matched);
    })().catch((error: unknown) => { if (active) setFeedback(`无法恢复科研任务：${errorText(error)}`); });
    return () => { active = false; };
  }, [initialConversationId, loading, project.id, runDetail, workflows]);

	async function startResearch() {
		const idea = researchIdea.trim();
		if (!idea || busy || referenceBusy || mutationRef.current) return;
		if (!modelProfileId || !modelId) { setFeedback("请先在顶部选择用于研究规划的 AI 模型。"); return; }
		mutationRef.current = "start-research"; setBusy("start-research"); setFeedback("");
		try {
			const started = normalizeWorkflowRunDetail(await backend<WorkflowRunDetail>("WorkflowFacade", "StartResearch", { projectId: project.id, researchIdea: idea, referenceAttachmentIds, modelProfileId, modelId, reasoningLevel }));
			activeWorkflowIdRef.current = started.run.workflowId; activeRunIdRef.current = started.run.id;
			const owner = normalizeWorkflowDetail(await backend<WorkflowDetail>("WorkflowFacade", "Get", project.id, started.run.workflowId));
			setDetail(owner); setRunDetail(started); setSelectedId(""); setSelectedTemplateId(""); setSelectedTemplateDefinition(null); chooseSideView("tasks"); setView("guide");
			if (started.run.conversationId) await selectConversation(started.run.conversationId);
			setReferenceAttachmentIds([]);
			await loadProjectRuns(); setResearchIdea(""); setFeedback("AI 正在读取当前项目资源并规划候选研究路线。完成后会在中间协作区显示路线卡。");
		} catch (error) { setFeedback(errorText(error)); }
		finally { if (mutationRef.current === "start-research") mutationRef.current = ""; setBusy(""); }
	}

	async function adoptResearchRoute(route: ResearchStarterRoute) {
		if (!runDetail || busy || mutationRef.current) return;
		if (starterPlanChecking) {
			setFeedback("正在核验这条路线的阶段、资源和 Skill 快照，请稍候再开始。");
			return;
		}
		const operation = `adopt-route:${route.routeId}`; mutationRef.current = operation; setBusy(operation); setFeedback(""); setSceneTransition("adopting");
		try {
			// Re-read the host projection immediately before adoption. The starter
			// card may have been rendered from an older polling snapshot (for
			// example after a resource or Skill changed), while the backend applies
			// the same validation again as its final gate.
			const validated = await backend<ResearchStarterPlan>("WorkflowFacade", "GetResearchStarterPlan", project.id, runDetail.run.id);
			setStarterPlan(validated);
			const currentRoute = validated.routes.find((value) => value.routeId === route.routeId);
			if (!currentRoute || !researchRouteCanAdopt(currentRoute)) {
				throw new Error(currentRoute?.validationError || "这条路线尚未通过宿主核验，暂时不能启动。");
			}
			const adopted = await backend<AdoptResearchRouteResult>("WorkflowFacade", "AdoptResearchRoute", { projectId: project.id, runId: runDetail.run.id, routeId: currentRoute.routeId });
			await loadList(adopted.workflow.workflow.id);
			const loaded = normalizeWorkflowDetail(await backend<WorkflowDetail>("WorkflowFacade", "Get", project.id, adopted.workflow.workflow.id));
			const adoptedTaskId = adopted.researchTaskId || adopted.run?.run.researchTaskId || adopted.starterRunId;
			activeWorkflowIdRef.current = loaded.workflow.id; activeRunIdRef.current = adopted.run?.run.id ?? ""; ++runRequestRef.current;
			if (adopted.run) adopted.run = normalizeWorkflowRunDetail(adopted.run);
			setDetail(loaded); setSelectedId(loaded.workflow.id); setSelectedTemplateId(""); setSelectedTemplateDefinition(null); setRunDetail(adopted.run ?? null); setPendingAdoptedRoute(adopted.run ? null : { starterRunId: adopted.starterRunId, researchTaskId: adoptedTaskId, researchGoal: adopted.researchIdea, routeId: adopted.routeId, workflowId: adopted.workflow.workflow.id, workflowVersionId: adopted.workflow.version.id }); chooseSideView(adopted.run ? "tasks" : "plans"); setView("guide");
			const current = loaded.versions.find((value) => value.id === loaded.workflow.currentVersionId) ?? loaded.versions[0];
			if (current) { setSelectedVersionId(current.id); setEditor(JSON.stringify(current.definition, null, 2)); setRunInputs(workflowInputDraft(current.definition, adopted.initialInputs)); }
			if (adopted.run?.run.conversationId) await selectConversation(adopted.run.run.conversationId);
			await loadProjectRuns();
			setFeedback(adopted.run ? `已采用“${loaded.workflow.name}”并开始正式科研任务。中间协作区会持续显示阶段进度和 AI 动作。` : `已建立“${loaded.workflow.name}”正式方案。请在当前任务设置中选择数据文件；外部文件会复制到当前项目后再开始任务。`);
			setSceneTransition("adopted"); sceneTimerRef.current = window.setTimeout(() => { setSceneTransition(""); sceneTimerRef.current = null; }, 900);
			if (!adopted.run) window.requestAnimationFrame(() => startPanelRef.current?.scrollIntoView({ behavior: "smooth", block: "center" }));
		} catch (error) { setSceneTransition(""); setFeedback(errorText(error)); }
		finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
	}

	async function answerResearchClarification(answers: Record<string, string[]>) {
		if (!runDetail || !starterPlan || busy || mutationRef.current || starterPlanChecking || starterPlanError || !validResearchClarification(starterPlan.clarification)) return;
		const requiredComplete = starterPlan.clarification.questions.filter((question) => question.required).every((question) => (answers[question.id] ?? []).length > 0);
		if (!requiredComplete) { setFeedback("请先完成所有必选问题。"); return; }
		const operation = "answer-clarification";
		mutationRef.current = operation; setBusy(operation); setFeedback("");
		try {
			const started = normalizeWorkflowRunDetail(await backend<WorkflowRunDetail>("WorkflowFacade", "AnswerResearchClarification", { projectId: project.id, starterRunId: runDetail.run.id, answers }));
			activeWorkflowIdRef.current = started.run.workflowId; activeRunIdRef.current = started.run.id;
			setStarterPlan(null); setStarterPlanChecking(false); setStarterPlanError(""); setRunDetail(started); chooseSideView("tasks"); setView("guide");
			if (started.run.conversationId) await selectConversation(started.run.conversationId);
			await loadProjectRuns();
			setFeedback("已记录你的选择，AI 正在为同一科研任务重新生成路线。");
		} catch (error) { setFeedback(errorText(error)); }
		finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
	}

  async function replanResearchStarter() {
    if (!runDetail || busy || mutationRef.current || starterPlanChecking || runDetail.run.status !== "completed" || runDetail.run.workflowPurpose !== "research_starter") return;
    const operation = "replan-research-starter";
    mutationRef.current = operation; setBusy(operation); setFeedback("");
    try {
      const started = normalizeWorkflowRunDetail(await backend<WorkflowRunDetail>("WorkflowFacade", "ReplanResearchStarter", { projectId: project.id, starterRunId: runDetail.run.id }));
      activeWorkflowIdRef.current = started.run.workflowId; activeRunIdRef.current = started.run.id;
      setStarterPlan(null); setStarterPlanChecking(false); setStarterPlanError(""); setRunDetail(started); chooseSideView("tasks"); setView("guide");
      if (started.run.conversationId) await selectConversation(started.run.conversationId);
      await loadProjectRuns();
      setFeedback("已重新读取当前任务资料，AI 正在重新规划；旧记录已保留。");
    } catch (error) { setFeedback(errorText(error)); }
    finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  function newFromTemplate(template?: WorkflowTemplate) {
	if (busy || mutationRef.current) return;
	const selected = template ?? workflowTemplates[0] ?? emptyWorkflowTemplate;
	chooseSideView("plans");
	++workflowRequestRef.current;
	activeWorkflowIdRef.current = "";
	++runRequestRef.current;
	activeRunIdRef.current = "";
	setPendingAdoptedRoute(null);
    editorTouched.current = true;
    setSelectedTemplateId(selected.id);
    setSelectedTemplateDefinition(selected.definition);
    setSelectedId("");
    setDetail(null);
    setPreview(null);
	setPendingAdoptedRoute(null);
    setFeedback(`正在预览模板“${selected.name}”。模板不会直接运行；保存到“我的方案”后，才可填写输入并创建任务。`);
    setView("guide");
    setSelectedVersionId("");
	setRunDetail(null);
	setPendingAdoptedRoute(null);
	setRunInputs(workflowInputDraft(selected.definition));
    setEditor(JSON.stringify(selected.definition, null, 2));
    window.requestAnimationFrame(() => guideRef.current?.scrollTo({ top: 0, behavior: "smooth" }));
  }

  function openPlanHome() {
	if (busy || mutationRef.current) return;
	chooseSideView("plans");
	++workflowRequestRef.current;
	++runRequestRef.current;
	activeWorkflowIdRef.current = "";
	activeRunIdRef.current = "";
	setSelectedId("");
	setSelectedTemplateId("");
	setSelectedTemplateDefinition(null);
	setTemplateDetail(null);
	setDetail(null);
	setRunDetail(null);
	setPendingAdoptedRoute(null);
	setPreview(null);
	setFeedback("");
	setView("guide");
  }

  function moveToPlanSection(target: "idea" | "templates" | "saved") {
    const element = target === "idea" ? planIdeaRef.current : target === "templates" ? planTemplateRef.current : savedPlanRef.current;
    element?.scrollIntoView({ behavior: "smooth", block: "start" });
    if (target === "idea") window.requestAnimationFrame(() => element?.querySelector("textarea")?.focus());
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
	const adopting = !detail && Boolean(selectedTemplateId);
	if (adopting) setSceneTransition("adopting");
	mutationRef.current = "save";
	setBusy("save");
    setFeedback("");
    try {
      const definition = parseDefinition();
      const checked = await backend<WorkflowPreview>("WorkflowFacade", "Validate", project.id, definition);
      setPreview(checked);
      if (!checked.valid) {
		if (adopting) setSceneTransition("");
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
	  ++runRequestRef.current;
	  setSelectedId(saved.workflow.id);
	      const loaded = normalizeWorkflowDetail(await backend<WorkflowDetail>("WorkflowFacade", "Get", project.id, saved.workflow.id));
      setDetail(loaded);
      setSelectedVersionId(saved.workflow.currentVersionId);
      await loadList(saved.workflow.id);
      setFeedback(saved.created ? "研究方案已保存，现在可以填写目标并开始任务。" : adopting ? `“${saved.workflow.name}”已在我的方案中，已直接打开，不会重复添加。` : saved.version.id === detail?.workflow.currentVersionId ? "研究方案没有变化，继续使用当前版本。" : `研究方案已保存为 v${saved.version.version}。`);
	  if (adopting) {
		setSceneTransition("adopted");
		sceneTimerRef.current = window.setTimeout(() => { setSceneTransition(""); sceneTimerRef.current = null; }, 900);
	  }
      window.requestAnimationFrame(() => startPanelRef.current?.scrollIntoView({ behavior: "smooth", block: "center" }));
    } catch (error) {
	  if (adopting) setSceneTransition("");
      setFeedback(errorText(error));
    } finally {
	  if (mutationRef.current === "save") mutationRef.current = "";
      setBusy("");
    }
  }

  async function deleteSelectedPlans() {
    if (busy || mutationRef.current) return;
    const selected = visibleWorkflows.filter(v=>planSelection.includes(v.id));
    if (!selected.length) return;
    mutationRef.current = "delete-plans"; setBusy("delete-plans");
    const deleted: string[] = [], failures: string[] = [];
    try {
      if (!await appConfirm({title:`删除选中的 ${selected.length} 个研究方案？`,message:"同时删除这些方案的全部版本、已结束任务、运行日志及科研会话；旧版重复方案也会一并清理。已登记科研产物保留。有未结束任务的方案不会删除。此操作无法撤销。",confirmLabel:"确认删除",tone:"danger"})) return;
      for (const value of selected) {
        try {await backend<void>("WorkflowFacade","Delete",project.id,value.id);deleted.push(value.id);}
        catch(error) {failures.push(`${value.name}：${errorText(error)}`);}
      }
      setPlanSelection(ids=>ids.filter(id=>!deleted.includes(id)));
      if(deleted.length) {
        const removed=workflows.filter(w=>deleted.includes(w.id)||selected.some(s=>deleted.includes(s.id)&&s.currentDefinitionSha256&&s.currentDefinitionSha256===w.currentDefinitionSha256));
        if(removed.some(w=>w.id===activeWorkflowIdRef.current)) {
          ++workflowRequestRef.current; ++runRequestRef.current;
          activeWorkflowIdRef.current=""; activeRunIdRef.current="";
          setSelectedId("");setDetail(null);setRunDetail(null);setSelectedVersionId("");setPreview(null);
          await selectConversation("");
        }
        await loadList(""); await loadProjectRuns();
      }
      setFeedback(`已删除 ${deleted.length} 个方案。${failures.length ? `未删除 ${failures.length} 个：\n${failures.join("\n")}` : "已登记科研产物保留。"}`);
    } catch(error) {setFeedback(`已删除 ${deleted.length} 个方案；刷新失败：${errorText(error)}`);}
    finally {if(mutationRef.current==="delete-plans")mutationRef.current="";setBusy("");}
  }

  async function deleteWorkflow(value: ResearchWorkflow) {
	if (busy || mutationRef.current) return;
	const inspection = `inspect-delete:${value.id}`;
	mutationRef.current = inspection;
	setBusy(inspection);
	setFeedback("");
	const group = value.currentDefinitionSha256 ? workflows.filter((item) => item.currentDefinitionSha256 === value.currentDefinitionSha256) : [value];
	let histories: { workflow: ResearchWorkflow; runs: WorkflowRun[] }[];
	try {
	  histories = await Promise.all(group.map(async (workflow) => ({ workflow, runs: await backend<WorkflowRun[]>("WorkflowFacade", "ListRuns", project.id, workflow.id, 100) })));
	  histories = histories.map((item) => ({ ...item, runs: normalizeWorkflowRunList(item.runs) }));
	} catch (error) {
	  setFeedback(`无法确认研究方案是否仍有未结束任务，未执行删除：${errorText(error)}`);
	  return;
	} finally {
	  if (mutationRef.current === inspection) mutationRef.current = "";
	  setBusy("");
	}
	const history = histories.flatMap((item) => item.runs);
	const activeRuns = history.filter((run) => !workflowTerminal(run.status));
	if (activeRuns.length > 0) {
	  const firstActiveRun = activeRuns[0];
	  const statusSummary = [...new Set(activeRuns.map((run) => workflowRunLabels[run.status]))].join("、");
	  const openTask = await appConfirm({ title: `暂时无法删除“${value.name}”`, message: `该方案仍有 ${activeRuns.length} 条未结束任务（${statusSummary}）。必须先进入任务并点击“取消”，让 AI、工具调用、授权或人工确认安全结束。\n\n程序禁止直接删除活动任务，因为这会破坏运行状态、审计记录和重启后的恢复闭环。`, confirmLabel: "打开未结束任务", cancelLabel: "暂不处理" });
	  if (openTask && firstActiveRun) {
		const owner = workflows.find((item) => item.id === firstActiveRun.workflowId) ?? value;
		await selectWorkflow(owner);
		await loadRun(firstActiveRun.id);
	  }
	  return;
	}
	const duplicateText = group.length > 1 ? `检测到 ${group.length} 份旧版重复方案，将作为同一项一并删除。\n` : "";
	const historyText = history.length ? `${duplicateText}同时会删除 ${history.length} 条已结束任务记录及其运行日志。` : `${duplicateText}同时会删除该方案的全部版本；该方案目前没有任务记录。`;
	if (!await appConfirm({ title: `删除研究方案“${value.name}”？`, message: `${historyText.trim()}\n此操作无法撤销。`, confirmLabel: "删除方案", tone: "danger" })) return;
	const operation = `delete:${value.id}`;
	mutationRef.current = operation;
	setBusy(operation);
	setFeedback("");
	try {
	  await backend<void>("WorkflowFacade", "Delete", project.id, value.id);
	  await selectConversation("");
	  if (group.some((item) => item.id === activeWorkflowIdRef.current)) {
		++workflowRequestRef.current; ++runRequestRef.current;
		activeWorkflowIdRef.current = ""; activeRunIdRef.current = "";
		setSelectedId(""); setDetail(null); setRunDetail(null);
		const initial = workflowTemplates[0] ?? emptyWorkflowTemplate;
		setSelectedTemplateId(initial.id); setSelectedTemplateDefinition(initial.definition);
		setEditor(JSON.stringify(initial.definition, null, 2)); setPreview(null); setSelectedVersionId("");
		setRunInputs(workflowInputDraft(initial.definition));
	  }
	  await loadList("");
	  await loadProjectRuns();
	  setFeedback(`已删除研究方案“${value.name}”${group.length > 1 ? `及 ${group.length - 1} 份旧版重复项` : ""}、任务历史及其科研会话；已经登记的科研产物仍保留在项目中。`);
	} catch (error) {
	  const message = errorText(error);
	  if (message.includes("未结束的科研任务") || message.includes("正在运行的 AI 协作")) {
		await appAlert({ title: `暂时无法删除“${value.name}”`, message: "必须先进入任务并点击“取消”，让正在进行的 AI、工具调用、授权或人工确认安全结束。直接删除会破坏运行状态、审计记录和重启后的恢复闭环。", confirmLabel: "知道了" });
	  } else {
		setFeedback(message);
	  }
	}
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
	  const pending = pendingAdoptedRoute && pendingAdoptedRoute.workflowId === detail?.workflow.id && pendingAdoptedRoute.workflowVersionId === currentVersion?.id ? pendingAdoptedRoute : null;
	  const selected = pending
		? await backend<WorkflowInputFile>("WorkflowFacade", "ChooseInputFileForResearchRoute", project.id, kind, pending.starterRunId)
		: await backend<WorkflowInputFile>("WorkflowFacade", "ChooseInputFile", project.id, kind);
      if (!selected?.relativePath) return;
      let paths: string[] = [];
      if (port.type === "array" && (port.maxItems ?? 1) > 1) {
        const parsed = JSON.parse(runInputs[port.name] || "[]");
        if (Array.isArray(parsed)) paths = parsed.filter((value): value is string => typeof value === "string");
      }
      paths = [...new Set([...paths, selected.relativePath])];
      if (paths.length > (port.maxItems ?? 16)) throw new Error("数据文件数量超过限制");
      updateRunInput(port.name, port.type === "string" ? selected.relativePath : JSON.stringify(paths));
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
		const pending = pendingAdoptedRoute && pendingAdoptedRoute.workflowId === detail.workflow.id && pendingAdoptedRoute.workflowVersionId === currentVersion.id ? pendingAdoptedRoute : null;
		const inputs = parseRunInputs();
		// The planner's research question is frozen at route adoption. Keep it
		// authoritative when a blocked route is resumed after file selection.
		if (pending) inputs.research_goal = pending.researchGoal.trim();
		const started = normalizeWorkflowRunDetail(pending
			? await backend<WorkflowRunDetail>("WorkflowFacade", "StartAdoptedResearchRoute", {
				projectId: project.id, starterRunId: pending.starterRunId, routeId: pending.routeId,
				workflowId: detail.workflow.id, workflowVersionId: currentVersion.id, inputs,
				permissionMode: runPermissionMode, modelProfileId, modelId, reasoningLevel,
			})
			: await backend<WorkflowRunDetail>("WorkflowFacade", "Start", {
				projectId: project.id, researchTaskId: "__new_research_task__", workflowId: detail.workflow.id, workflowVersionId: currentVersion.id, inputs, permissionMode: runPermissionMode,
				modelProfileId, modelId, reasoningLevel,
			}));
	  activeRunIdRef.current = started.run.id;
		setPendingAdoptedRoute(null);
	  setRunDetail(started); chooseSideView("tasks"); setView("guide");
      if (started.run.conversationId) await selectConversation(started.run.conversationId);
	  await loadProjectRuns();
      setFeedback("科研任务已经开始。中间协作区已绑定本任务，AI 会按当前研究阶段使用工具。");
    } catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === "start-run") mutationRef.current = ""; setBusy(""); }
  }

  async function runAction(action: "Pause" | "Resume" | "Cancel") {
	if (!runDetail || busy || mutationRef.current) return;
	const operation = action.toLowerCase();
	mutationRef.current = operation;
	setBusy(operation); setFeedback("");
    try {
	  const updated = normalizeWorkflowRunDetail(await backend<WorkflowRunDetail>("WorkflowFacade", action, project.id, runDetail.run.id));
	  setRunDetail(updated);
	  await loadProjectRuns();
      setFeedback(action === "Pause" ? "任务已在当前检查点暂停。" : action === "Resume" ? "科研任务已继续。" : "已请求取消科研任务。");
    } catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  async function decide(step: WorkflowStep, approved: boolean, continueWithoutCitations = false, acceptLimited = false) {
	if (!runDetail || busy || mutationRef.current) return;
	const operation = `decide:${step.id}`;
	mutationRef.current = operation;
	setBusy(operation); setFeedback("");
    try {
      let context: unknown = {};
      if (step.nodeKind === "candidate_selection") context = { selectedCandidateIds, selectedAttachmentIds };
      else if (step.nodeKind === "citation_selection") {
        const selected = new Set(selectedCitationKeys);
        context = workflowCitations(step).filter((value) => selected.has(JSON.stringify(value)));
      } else if (decisionContext.trim()) {
        try { context = JSON.parse(decisionContext); } catch { throw new Error("人工决定上下文必须是有效 JSON。"); }
      }
	      const updated = normalizeWorkflowRunDetail(await backend<WorkflowRunDetail>("WorkflowFacade", "Decide", {
		projectId: project.id, runId: runDetail.run.id, stepId: step.id, approved, continueWithoutCitations, acceptLimitedEvidence: acceptLimited, note: decisionNote, context,
	      }));
      setRunDetail(updated);
	  await loadProjectRuns();
    } catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  async function resolveWorkflowApproval(approval: Approval, allow: boolean) {
	if (busy || mutationRef.current) return;
	const operation = `approval:${approval.id}`;
	mutationRef.current = operation;
	setBusy(operation); setFeedback("");
    try {
	  const updated = normalizeWorkflowRunDetail(await backend<WorkflowRunDetail>("WorkflowFacade", "ResolveApproval", { approvalId: approval.id, allow, scope: "call" }));
	  setRunDetail(updated); await loadProjectRuns();
    } catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  async function resolveAIApproval(approval: Approval, allow: boolean) {
    if (!runDetail || busy || mutationRef.current) return;
    const operation = `ai-approval:${approval.id}`;
    mutationRef.current = operation;
    setBusy(operation); setFeedback("");
    try {
      await backend("PermissionFacade", "ResolveApproval", { approvalId: approval.id, allow, scope: "call" });
      // The approval belongs to the hidden Chat Run bound to this AI stage.
      // Refresh the Workflow projection after resuming that Chat Run; the
      // normal 700 ms poll remains the source of truth if the model has not
      // started its next turn yet.
      await loadRun(runDetail.run.id, true);
    } catch (error) { setFeedback(errorText(error)); }
    finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  async function retryStep(step: WorkflowStep, revisionNodeId = "", useRecommendation = false, expectedReviewSha256 = "") {
	if (!runDetail || busy || mutationRef.current) return;
    const node = runDetail.run.compilation.nodes.find((value) => value.id === step.nodeId);
    const repeatsSideEffects = Boolean(node?.sideEffect || runDetail.reviewRevisionTargets?.find((target) => target.nodeId === revisionNodeId)?.repeatsSideEffects);
    if (repeatsSideEffects && !confirmRetry) { setFeedback("该步骤可能有副作用，请先确认重新执行。"); return; }
	const operation = `retry:${step.id}`;
	mutationRef.current = operation;
	setBusy(operation); setFeedback(node?.tool?.qualifiedName === "builtin.research.workflow.review.gate" ? "正在准备按审查意见修订上一版结果…" : "正在准备重新执行当前阶段…");
    try {
	      const updated = normalizeWorkflowRunDetail(await backend<WorkflowRunDetail>("WorkflowFacade", "Retry", {
		projectId: project.id, runId: runDetail.run.id, stepId: step.id, revisionNodeId, useRecommendation, expectedReviewSha256, confirmSideEffect: Boolean(repeatsSideEffects && confirmRetry), note: repeatsSideEffects ? "用户在科研模式中确认重新执行可能产生副作用的步骤" : "",
	  }));
	  setRunDetail(updated); setConfirmRetry(false); await loadProjectRuns();
	  // The backend queues the revision before its driver starts. Refresh the
	  // authoritative detail so the interaction card changes immediately.
	  await loadRun(updated.run.id, true);
	  if (node?.tool?.qualifiedName === "builtin.research.workflow.review.gate") setFeedback("已进入修订回路：正在依据独立审查意见修订上一版结果，随后会自动重新审查。");
    } catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  async function confirmResearchRevision(proposalId: string) {
    if (!runDetail || busy || mutationRef.current) return;
    const operation = "confirm-revision";
    mutationRef.current = operation;
    setBusy(operation); setFeedback("");
    try {
      const updated = normalizeWorkflowRunDetail(await backend<WorkflowRunDetail>("WorkflowFacade", "ConfirmResearchRevision", { projectId: project.id, runId: runDetail.run.id, proposalId, confirmed: true }));
      ++runRequestRef.current;
      setRunDetail(updated);
      await loadProjectRuns();
    } catch (error) { setFeedback(errorText(error)); }
    finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  async function saveWorkflowDeliverable(outputName: "research_design" | "report_draft") {
	if (!runDetail || busy || mutationRef.current || outputName === "research_design" && !workflowResearchDesign(runDetail) || outputName === "report_draft" && !workflowResearchReport(runDetail)) return;
	const operation = "save-deliverable";
	mutationRef.current = operation;
	setBusy(operation); setFeedback("");
	try {
	  await backend<ArtifactSaveResult>("WorkflowFacade", "SaveDeliverable", { projectId: project.id, runId: runDetail.run.id, outputName });
	  await loadRun(runDetail.run.id);
	  await loadProjectRuns();
	  setFeedback(outputName === "report_draft" ? "研究交付稿已登记为不可变 Markdown 科研产物，可在科研产物中查看、校验和导出。" : "研究设计已登记为不可变 Markdown 科研产物，可在科研产物中查看、校验和导出。");
	} catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  async function deleteRun(run: WorkflowRun) {
	if (busy || mutationRef.current || !workflowTerminal(run.status)) return;
	if (!await appConfirm({ title: "删除这条科研任务记录？", message: `任务 ${run.id.slice(0, 8)} 的阶段、运行日志和独立科研会话会一并删除；已经登记的科研产物仍会保留。\n此操作无法撤销。`, confirmLabel: "删除任务", tone: "danger" })) return;
	const operation = `delete-run:${run.id}`;
	mutationRef.current = operation;
	setBusy(operation); setFeedback("");
	try {
	  await backend<void>("WorkflowFacade", "DeleteRun", project.id, run.id);
	  const deletingSelected = activeRunIdRef.current === run.id;
	  if (deletingSelected) {
		++runRequestRef.current; activeRunIdRef.current = "";
		setRunDetail(null); setView("guide");
		await selectConversation("");
	  }
	  await loadProjectRuns();
	  setFeedback("已删除科研任务、运行日志及其独立科研会话；已经登记的科研产物仍保留。");
	} catch (error) { setFeedback(errorText(error)); }
	finally { if (mutationRef.current === operation) mutationRef.current = ""; setBusy(""); }
  }

  const startPanel = (<section className="research-start-panel" ref={startPanelRef}><header><div><b>{detail ? pendingAdoptedRoute ? "已选路线 · 补充数据" : "用此方案创建科研任务" : "保存为我的方案"}</b><small>{detail ? pendingAdoptedRoute ? "路线已确定，等待数据文件。" : "这里创建的是一次独立任务；输入、方案版本与工具权限会在启动时冻结。" : "保存方案不会启动任务。保存完成后，再填写这一次任务的输入。"}</small></div>{detail && <span>我的方案 v{detail.workflow.version}</span>}</header>{detail ? <div className="research-guide-inputs"><label className={`workflow-permission-mode ${runPermissionMode}`}><span><Icon name="shield" size={15}/><div><b>任务工具权限</b><small>{runPermissionMode === "full_access" ? "默认放开已注册工具，仍受 Workspace 路径、Schema 和执行边界保护" : "写入、进程与高风险工具会逐次请求确认"}</small></div></span><select value={runPermissionMode} disabled={Boolean(busy)} onChange={(event) => setRunPermissionMode(event.target.value as PermissionMode)}><option value="full_access">Full Access · 自动执行</option><option value="plan">Plan · 逐次确认</option></select></label>{needsPython && <div className={`workflow-python-preflight ${pythonPreflight}`}><Icon name={pythonPreflight === "ready" || pythonPreflight === "available" ? "check" : pythonPreflight === "checking" ? "refresh" : "tool"} size={15}/><span><b>{pythonPreflight === "ready" ? "项目 Python 运行环境已就绪" : pythonPreflight === "available" ? "启动后将自动创建项目 Python 运行环境" : pythonPreflight === "missing" ? "未检测到可用的 Python 3" : pythonPreflight === "error" ? "无法读取 Python 环境状态" : "正在检查 Python 环境"}</b><small>{pythonPreflight === "missing" ? "请先安装 Python 3，或在 Python 环境中绑定已有虚拟环境。" : "系统 Python 只负责创建；分析工具使用项目隔离环境，代码、依赖和指纹会进入复现记录。"}</small></span><button type="button" onClick={openPython}>{pythonPreflight === "missing" ? "设置环境" : "查看环境"}</button></div>}{inputDefinition?.inputs.length ? <WorkflowInputFields frozenRoute={Boolean(pendingAdoptedRoute)} definition={inputDefinition} values={runInputs} disabled={Boolean(busy)} chooseFile={(port) => void chooseInputFile(port)} update={updateRunInput}/> : <p>这套研究方案无需额外输入，可以直接开始。</p>}<div className="research-launch-row"><span className={inputReady ? "ready" : "waiting"}><Icon name={inputReady ? "check" : "history"} size={14}/>{inputReady ? `任务输入已就绪 · ${runPermissionMode === "full_access" ? "Full Access" : "Plan"}` : "请先完成必填输入"}</span><button type="button" disabled={!currentVersion || !inputReady || needsPython && (pythonPreflight === "missing" || pythonPreflight === "checking") || Boolean(busy)} onClick={() => void startRun()}><Icon name="play" size={15}/>{busy === "start-run" ? "正在启动任务…" : pendingAdoptedRoute ? "开始执行所选路线" : "创建并开始任务"}</button></div></div> : <button type="button" className="research-adopt-plan" disabled={Boolean(busy)} onClick={() => void save()}><Icon name="check" size={15}/>{busy === "save" ? "正在保存方案…" : "保存到我的方案"}</button>}</section>);
  const interaction = runDetail ? (<WorkflowInteractionCards detail={runDetail} selectedAttachmentIds={selectedAttachmentIds} setSelectedAttachmentIds={setSelectedAttachmentIds} starterPlan={starterPlan} starterPlanChecking={starterPlanChecking} starterPlanError={starterPlanError} pendingDecisionStep={pendingDecisionStep} note={decisionNote} context={decisionContext} selectedCandidateIds={selectedCandidateIds} selectedCitationKeys={selectedCitationKeys} acceptLimitedEvidence={acceptLimitedEvidence} confirmRetry={confirmRetry} busy={busy} setNote={setDecisionNote} setContext={setDecisionContext} setSelectedCandidateIds={setSelectedCandidateIds} setSelectedCitationKeys={setSelectedCitationKeys} setAcceptLimitedEvidence={setAcceptLimitedEvidence} setConfirmRetry={setConfirmRetry} resolveApproval={(approval, allow) => void resolveWorkflowApproval(approval, allow)} decide={(step, approved, continueWithoutCitations, acceptLimited) => void decide(step, approved, continueWithoutCitations, acceptLimited)} retryStep={(step, revisionNodeId, recommended, reviewSha) => void retryStep(step, revisionNodeId, recommended, reviewSha)} adoptResearchRoute={(route) => void adoptResearchRoute(route)} answerClarification={(answers) => void answerResearchClarification(answers)} replanResearchStarter={() => void replanResearchStarter()}/>) : null;
  const renderTimelineRef = useRef<(entry: ResearchTimelineEntry) => ReactNode>(() => null);
  renderTimelineRef.current = entry => {
    const current = runDetail?.run.id === entry.runId;
    const currentEvent = current && entry.kind === "event" && runDetail.events.filter(event => event.type === entry.eventType && String(event.payload.stepId ?? "") === String(entry.snapshot.event?.stepId ?? "")).at(-1)?.id === entry.id.slice("event:".length);
    if (entry.tool) {
      const call = current ? runDetail.toolActivities?.find(call => call.id === entry.tool!.id) ?? entry.tool : entry.tool;
      return <WorkflowTimelineToolCard item={{call, label: entry.snapshot.nodeName || call.summary || call.toolName, busy,
        approval: current ? runDetail.pendingApprovals.find(approval => approval.toolCallId === call.id) : undefined,
        resolveApproval: (approval, allow) => void resolveWorkflowApproval(approval, allow)}}/>;
    }
    if (entry.proposal) {
      const proposal = current ? runDetail.revisionProposals?.find(proposal => proposal.id === entry.proposal!.id) ?? entry.proposal : entry.proposal;
      return <ResearchRevisionCards proposals={[{...proposal, canConfirm: Boolean(current && proposal.canConfirm)}]} busy={Boolean(busy)} conversationId={current ? runDetail.run.conversationId ?? "" : ""} confirm={id => void confirmResearchRevision(id)}/>;
    }
    if (entry.active && current && currentEvent) {
      if (entry.eventType === "workflow.completed" && runDetail.run.status === "completed") {
        if (runDetail.run.workflowPurpose === "research_starter" && !runDetail.events.some(event => event.type === "research.route_adopted")) return interaction;
        const report = workflowResearchReport(runDetail), design = workflowResearchDesign(runDetail);
        if (report) return <WorkflowResearchReportCard assessment={runDetail.deliveryAssessment} deliveryLabel={runDetail.deliveryAssessment?.label ?? "研究交付稿"} report={report} citations={reportCitationSnapshot(runDetail.run.outputs)} registered={workflowDeliverableRegistered(runDetail, "report_draft")} artifactCount={runDetail.artifactCount} taskId={runDetail.run.researchTaskId} busy={busy} saveDeliverable={() => void saveWorkflowDeliverable("report_draft")} openArtifacts={openArtifacts}/>;
        if (design) return <WorkflowResearchDesignCard assessment={runDetail.deliveryAssessment} design={design} registered={workflowDeliverableRegistered(runDetail, "research_design")} taskId={runDetail.run.researchTaskId} busy={busy} saveDeliverable={() => void saveWorkflowDeliverable("research_design")} openArtifacts={openArtifacts}/>;
        return <><ResearchTimelineHistory entry={entry}/><ResearchDeliveryStatus assessment={runDetail.deliveryAssessment} runStatus={runDetail.run.status}/></>;
      }
      if (entry.step && runDetail.steps.some(step => step.id === entry.step!.id && step.attempt === entry.step!.attempt &&
        (["workflow.human_confirmation_requested", "workflow.agent_stage_review_requested"].includes(entry.eventType ?? "")
          ? step.status === "waiting_human_confirmation" && runDetail.run.status === "waiting_human_confirmation"
          : ["failed", "interrupted", "outcome_unknown"].includes(step.status) && ["failed", "interrupted"].includes(runDetail.run.status)))) return interaction;
    }
    if (entry.active && entry.eventType === "research.route_adopted" && pendingAdoptedRoute?.starterRunId === entry.runId && entry.snapshot.event?.routeId === pendingAdoptedRoute.routeId) return startPanel;
    return <ResearchTimelineHistory entry={entry}/>;
  };
  const timelineTaskId = runDetail?.run.researchTaskId || pendingAdoptedRoute?.researchTaskId || "";
  useEffect(() => {
    publishTaskTimeline(timelineTaskId ? {projectId: project.id, taskId: timelineTaskId, render: entry => renderTimelineRef.current(entry)} : null);
  }, [timelineTaskId, runDetail, pendingAdoptedRoute, busy, starterPlan, starterPlanChecking, starterPlanError, decisionNote, decisionContext, selectedCandidateIds, selectedAttachmentIds, selectedCitationKeys, acceptLimitedEvidence, confirmRetry, runInputs, runPermissionMode, pythonPreflight, publishTaskTimeline]);
  useEffect(() => () => publishTaskTimeline(null), [publishTaskTimeline]);

  if (pendingAdoptedRoute && !runDetail) return <aside className="research-route-panel">
    <header><div><p>RESEARCH TASK</p><h2>{detail?.workflow.name ?? "已采纳研究路线"}</h2><small>等待补充数据</small></div><button type="button" aria-label="返回研究方案" onClick={openPlanHome}><Icon name="back" size={15}/></button></header>
    {feedback && <p className="research-guide-feedback">{feedback}</p>}
  </aside>;

  if (runDetail) {
	const hasCompletedPlanning = Boolean(runDetail.run.researchStarterRunId);
    const completedSteps = runDetail.steps.filter((step) => step.status === "completed").length + (hasCompletedPlanning ? 1 : 0);
	const totalSteps = runDetail.steps.length + (hasCompletedPlanning ? 1 : 0);
    const taskQuestion = workflowTaskQuestion(runDetail.run);
	const researchStarter = runDetail.run.workflowPurpose === "research_starter";
    return <>
      <aside className="research-route-panel" aria-label="科研任务路线">
        <header><div><p>RESEARCH TASK</p><h2 title={workflowTaskQuestion(runDetail.run)?.text}>{workflowTaskTitle(runDetail.run)}</h2><small>{completedSteps}/{totalSteps} 阶段完成</small></div><button type="button" title="返回科研任务列表" aria-label="返回科研任务列表" onClick={() => { restoreConversationRef.current = initialConversationId; setRunDetail(null); chooseSideView("tasks"); setView("guide"); void selectConversation(""); }}><Icon name="back" size={15}/></button></header>
        {taskQuestion && <section className="research-task-question"><header><span>{taskQuestion.label}</span><Icon name="search" size={13}/></header><p title={taskQuestion.text}>{taskQuestion.text}</p><button type="button" onClick={() => void appAlert({ title: taskQuestion.label, message: taskQuestion.text, confirmLabel: "关闭" })}>查看完整输入</button></section>}
        <div className="research-route-status"><span className={`workflow-status ${runDetail.run.status}`}>{workflowRunStatusLabel(runDetail.run)}</span><b>任务 {runDetail.run.id.slice(0, 8)}</b><small>{runDetail.run.permissionMode === "full_access" ? "Full Access" : "Plan"} · {new Date(runDetail.run.updatedAt).toLocaleString()}</small></div>
        <section className="research-route-steps"><header><b>研究路线</b><span>{Math.round(completedSteps / Math.max(totalSteps, 1) * 100)}%</span></header><div>{hasCompletedPlanning && <article className="completed"><i><Icon name="check" size={12}/></i><span><b><span>AI 路线规划</span></b></span></article>}{runDetail.steps.map((step) => { const node = runDetail.run.compilation.nodes.find((value) => value.id === step.nodeId); return <article key={step.id} className={step.status} title={workflowStepLabels[step.status]}><i>{step.status === "completed" ? <Icon name="check" size={12}/> : step.ordinal + 1 + (hasCompletedPlanning ? 1 : 0)}</i><span><b><span>{node?.name || `阶段 ${step.ordinal + 1}`}</span>{step.attempt > 1 && <em title={`已执行 ${step.attempt} 次`}>{step.attempt}</em>}</b></span></article>; })}</div></section>
        <footer>{runDetail.run.status === "paused" && <button type="button" onClick={() => void runAction("Resume")} disabled={Boolean(busy)}><Icon name="play" size={13}/>继续任务</button>}{["queued", "waiting_approval", "waiting_human_confirmation"].includes(runDetail.run.status) && <button type="button" onClick={() => void runAction("Pause")} disabled={Boolean(busy)}><Icon name="pause" size={13}/>暂停</button>}{!workflowTerminal(runDetail.run.status) && <button type="button" className="danger" onClick={() => void runAction("Cancel")} disabled={Boolean(busy)}><Icon name="stop" size={12}/>取消</button>}</footer>
      </aside>
      <aside className="research-evidence-panel" aria-label="科研任务核验与成果">
        <header><div><p>VERIFIABLE RESULTS</p><h2>核验与成果</h2></div><button type="button" aria-label="刷新当前任务" title="刷新" onClick={() => void loadRun(runDetail.run.id)}><Icon name="refresh" size={14}/></button></header>
        {runDetail.run.status === "completed" && (researchStarter ? <section className="workflow-run-output starter"><header><div><h3>研究路线规划已完成</h3><small>请在中间协作区选择一条路线，建立正式研究方案。</small></div></header></section> : runDetail.deliveryAssessment?.status === "unverified" ? <section className="workflow-run-output"><header><div><h3>流程结束，交付尚未验证</h3><small>{runDetail.deliveryAssessment.summary}</small></div></header></section> : workflowResearchReport(runDetail) ? <section className="workflow-run-output deliverable"><header><div><h3>{runDetail.deliveryAssessment?.label ?? "研究交付稿"}已完成</h3><small>已通过独立审查；在中间协作区查看报告正文并登记科研产物。</small></div>{workflowDeliverableRegistered(runDetail, "report_draft") ? <button type="button" onClick={() => openArtifacts(runDetail.run.researchTaskId)}><Icon name="check" size={13}/>已登记 · 查看科研产物</button> : runDetail.artifactCount > 0 && <button type="button" onClick={() => openArtifacts(runDetail.run.researchTaskId)}><Icon name="archive" size={13}/>查看科研产物</button>}</header></section> : workflowResearchDesign(runDetail) ? <section className="workflow-run-output deliverable"><header><div><h3>研究设计已完成</h3><small>已通过独立审查；这是研究方案，尚不是实证结论。</small></div>{workflowDeliverableRegistered(runDetail, "research_design") ? <button type="button" onClick={() => openArtifacts(runDetail.run.researchTaskId)}><Icon name="check" size={13}/>已登记 · 查看科研产物</button> : <button type="button" disabled={Boolean(busy)} onClick={() => void saveWorkflowDeliverable("research_design")}>{busy === "save-deliverable" ? "正在登记…" : "登记科研产物"}</button>}</header></section> : <section className="workflow-run-output"><header><div><h3>科研流程已完成</h3><small>{runDetail.artifactCount > 0 ? `已登记 ${runDetail.artifactCount} 个科研产物。` : "结果已保存，本次流程未生成文件型科研产物。"}</small></div>{runDetail.artifactCount > 0 && <button type="button" onClick={() => openArtifacts(runDetail.run.researchTaskId)}><Icon name="archive" size={13}/>查看科研产物</button>}</header></section>)}
        <div className="research-task-connection"><i className={workflowTerminal(runDetail.run.status) ? "terminal" : "active"}/><span><b>{workflowTerminal(runDetail.run.status) ? "任务已结束" : "结果随任务实时更新"}</b><small>所有决定请在中间协作区完成；右侧只展示交付状态、来源与成果</small></span></div>
        {feedback && <div className="research-guide-feedback"><Icon name="shield" size={13}/><span>{feedback}</span></div>}
        {runDetail.run.errorMessage && <div className="workflow-run-error"><Icon name="stop" size={14}/><span><b>任务执行失败</b><small>{runDetail.steps.some((step) => step.status === "failed" && runDetail.run.compilation.nodes.some((node) => node.id === step.nodeId && node.tool?.qualifiedName === "builtin.research.workflow.review.gate")) ? "报告还需要修改，暂未交付。请在中间查看修改说明。" : runDetail.run.errorMessage}</small></span></div>}
        {!researchStarter && <WorkflowRunEvidence detail={runDetail} openArtifacts={(taskId) => openArtifacts(taskId)}/>}
      </aside>
    </>;
  }

  return <section className={`research-mode-workspace ${sceneTransition ? `scene-${sceneTransition}` : ""}`} aria-label={`${project.name} 科研模式`}>
    {templateDetail && <WorkflowTemplateDialog
      template={templateDetail}
      disabled={Boolean(busy)}
      close={() => setTemplateDetail(null)}
      useTemplate={() => {
        const selected = templateDetail;
        setTemplateDetail(null);
        newFromTemplate(selected);
      }}
    />}
    {sceneTransition && <div className="research-scene-curtain" aria-hidden="true"><span><i/><i/><i/></span></div>}
    <header className="research-mode-hero">
      <div className="research-mode-mark" aria-hidden="true"><span/><i/><Icon name="spark" size={23}/></div>
      <div><p>SCIAIDE RESEARCH MODE</p><h1>科研模式</h1><small>把研究目标转化为可跟随、可暂停、可核验的研究任务</small></div>
    </header>
    <div className="workflow-layout">
      <aside className={`workflow-side ${sideView}`}>
        <nav className="workflow-side-tabs" aria-label="科研模式导航"><button type="button" className={sideView === "tasks" ? "selected" : ""} aria-pressed={sideView === "tasks"} onClick={() => chooseSideView("tasks")}><Icon name="history" size={14}/><span>科研任务<small>{sideView === "loading" ? "正在读取…" : `${projectRuns.length} 条运行记录`}</small></span></button><button type="button" className={sideView === "plans" ? "selected" : ""} aria-pressed={sideView === "plans"} onClick={openPlanHome}><Icon name="library" size={14}/><span>研究方案<small>创建新任务</small></span></button></nav>
		{sideView === "loading" ? <div className="workflow-entry-loading"><Icon name="refresh" size={18}/><b>正在准备科研模式</b><span>检查当前项目是否已有科研任务…</span></div> : sideView === "tasks" ? <section className="workflow-project-tasks"><header><div><p>RESEARCH TASKS</p><h2>科研任务</h2></div><button type="button" title="刷新科研任务" aria-label="刷新科研任务" disabled={Boolean(busy) || projectRunsLoading} onClick={() => void loadProjectRuns()}><Icon name="refresh" size={14}/></button></header><small>每一项对应一个研究问题。AI 路线规划与后续执行会作为同一任务连续推进。</small><div>{projectRunsLoading ? <p>正在读取科研任务…</p> : projectRuns.length ? projectRuns.map((run) => <article key={run.id}><button type="button" disabled={Boolean(busy)} onClick={() => void openProjectRun(run)}><span><b title={workflowTaskQuestion(run)?.text}>{workflowTaskTitle(run)}</b><small>{run.workflowName || "研究方案"} · {new Date(run.createdAt).toLocaleString()}</small></span><em className={run.status}>{workflowRunStatusLabel(run)}</em></button>{workflowTerminal(run.status) && <button type="button" className="research-run-delete" disabled={Boolean(busy)} aria-label={`删除科研任务 ${run.id.slice(0, 8)}`} title="删除任务及科研会话" onClick={() => void deleteRun(run)}><Icon name={busy === `delete-run:${run.id}` ? "refresh" : "trash"} size={12}/></button>}</article>) : <div className="workflow-task-empty"><Icon name="history" size={20}/><b>还没有科研任务</b><span>输入一个研究想法，或选择方案模板创建第一条任务。</span><button type="button" onClick={openPlanHome}>开始研究</button></div>}</div></section> : detail ? <section className="workflow-plan-side-guide workflow-setup-side-guide"><header><small>SET UP THIS TASK</small><h2>补充任务输入</h2></header><p>当前路线已经采用，尚未启动科研任务。请在中间设置区选择本课题的数据文件；外部文件会复制到当前项目并生成可复现快照。</p><nav aria-label="任务设置导航"><button type="button" onClick={() => window.requestAnimationFrame(() => startPanelRef.current?.scrollIntoView({ behavior: "smooth", block: "center" }))}><i>01</i><span><b>选择数据文件</b><small>绑定 CSV、TSV 或 XLSX</small></span></button><button type="button" onClick={openPlanHome}><i>02</i><span><b>返回研究方案</b><small>创建其他科研任务</small></span></button></nav></section> : <section className="workflow-plan-side-guide"><header><small>START HERE</small><h2>选择开始方式</h2></header><nav aria-label="研究方案快速导航"><button type="button" onClick={() => moveToPlanSection("idea")}><i>01</i><span><b>描述研究问题</b><small>由 AI 规划路线</small></span></button><button type="button" onClick={() => moveToPlanSection("templates")}><i>02</i><span><b>直接使用模板</b><small>查看标准研究流程</small></span></button><button type="button" onClick={() => moveToPlanSection("saved")}><i>03</i><span><b>继续已有方案</b><small>{visibleWorkflows.length ? `${visibleWorkflows.length} 个可用方案` : "暂无已保存方案"}</small></span></button></nav></section>}
      </aside>
      {sideView === "loading" ? <main className="workflow-task-home loading"><section><div className="workflow-task-home-icon"><Icon name="refresh" size={25}/></div><p>RESEARCH MODE</p><h2>正在准备研究工作台</h2><span>正在读取当前项目的科研任务与研究方案。</span></section></main> : sideView === "tasks" ? <main className="workflow-task-home">
        <section><div className="workflow-task-home-icon"><Icon name="history" size={25}/></div><p>RESEARCH TASKS</p><h2>从左侧继续一项科研任务</h2><span>任务从路线规划到研究交付连续推进，点击即可查看当前进度、协作对话和结果。</span><div className="workflow-task-summary"><article><b>{projectRuns.length}</b><small>全部任务</small></article><article><b>{activeProjectRuns.length}</b><small>进行中</small></article><article><b>{projectRuns.filter((run) => run.status === "completed").length}</b><small>已完成</small></article></div><button type="button" onClick={openPlanHome}><Icon name="plus" size={15}/>创建新的科研任务</button></section>
      </main> : !detail && !selectedTemplateId ? <main className="workflow-plan-home">
        <header><div><p>新建研究</p><h2>从一个问题开始</h2></div><aside><b>{visibleWorkflows.length}</b><small>我的方案</small></aside></header>
        <section className="workflow-plan-idea" ref={planIdeaRef}><div><span><Icon name="spark" size={18}/></span><div><b>输入研究问题</b></div></div><textarea rows={3} value={researchIdea} maxLength={8000} disabled={Boolean(busy)} placeholder="例如：我想研究夜间使用手机是否影响大学生睡眠，但目前还没有数据和文献。" onChange={(event) => { setResearchIdea(event.target.value); setFeedback(""); }}/><ReferenceMaterials key={project.id} projectId={project.id} selected={referenceAttachmentIds} onChange={setReferenceAttachmentIds} disabled={Boolean(busy)} service={backend} onBusyChange={setReferenceBusy}/><footer><small>{(!modelProfileId || !modelId) ? "请先在顶部选择协作模型" : researchIdea.trim() ? `${[...researchIdea.trim()].length}/8000` : ""}</small><button type="button" disabled={!researchIdea.trim() || !modelProfileId || !modelId || Boolean(busy) || referenceBusy} onClick={() => void startResearch()}>{busy === "start-research" ? <Icon name="refresh" size={15}/> : <Icon name="send" size={15}/>}<span>{busy === "start-research" ? "正在规划" : "生成研究路线"}</span></button></footer></section>
        <section className="workflow-plan-home-section" ref={planTemplateRef}><header><h3>方案模板</h3><span>{workflowTemplates.length} 个模板</span></header><div className="workflow-template-gallery">{workflowTemplates.map((template, index) => <button type="button" key={template.id} disabled={Boolean(busy)} onClick={() => setTemplateDetail(template)}><i><Icon name={template.definition.nodes.some((node) => node.kind === "python") ? "chart" : index % 2 ? "shield" : "library"} size={19}/></i><span><b>{template.name}</b><small>{template.definition.nodes.length} 个阶段 · {template.definition.nodes.some((node) => node.kind === "python") ? "包含数据分析" : "研究设计与证据"}</small></span><em>查看详情</em></button>)}</div></section>
<section className="workflow-plan-home-section saved" ref={savedPlanRef}><header><h3>我的方案</h3><div className="workflow-plan-batch">{selectingPlans && <><label><input type="checkbox" aria-label="全选研究方案" disabled={Boolean(busy) || loading} checked={visibleWorkflows.length>0 && visibleWorkflows.every(v=>planSelection.includes(v.id))} onChange={e=>setPlanSelection(e.target.checked ? visibleWorkflows.map(v=>v.id) : [])}/>全选</label><button type="button" disabled={Boolean(busy) || !visibleWorkflows.some(v=>planSelection.includes(v.id))} onClick={()=>void deleteSelectedPlans()}><Icon name="trash" size={14}/>删除所选（{visibleWorkflows.filter(v=>planSelection.includes(v.id)).length}）</button></>}<button type="button" disabled={Boolean(busy) || loading} onClick={()=>{setSelectingPlans(v=>!v);setPlanSelection([]);}}>{selectingPlans ? "取消多选" : "多选"}</button></div><button type="button" title="刷新我的方案" aria-label="刷新我的方案" disabled={Boolean(busy) || loading} onClick={() => void loadList()}><Icon name="refresh" size={14}/></button></header><div className="workflow-saved-plan-list">{loading ? <p>正在读取我的方案…</p> : visibleWorkflows.length ? visibleWorkflows.map((value) => <article key={value.id}>{selectingPlans && <input className="workflow-plan-checkbox" type="checkbox" aria-label={`选择研究方案 ${value.name}`} checked={planSelection.includes(value.id)} disabled={Boolean(busy)} onChange={e=>setPlanSelection(ids=>e.target.checked ? [...ids,value.id] : ids.filter(id=>id!==value.id))}/>}<button type="button" disabled={Boolean(busy)} onClick={() => void selectWorkflow(value)}><i><Icon name="library" size={15}/></i><span><b>{value.name}</b><small>{value.description || "未填写方案说明"}</small></span><em>v{value.version}</em></button><button type="button" className="workflow-delete" disabled={Boolean(busy)} aria-label={`删除研究方案 ${value.name}`} title="删除方案及任务历史" onClick={() => void deleteWorkflow(value)}><Icon name={busy === `delete:${value.id}` ? "refresh" : "trash"} size={13}/></button></article>) : <div className="workflow-plan-home-empty"><Icon name="library" size={19}/><span><b>暂无已保存方案</b><small>使用模板后会显示在这里。</small></span></div>}</div></section>
        {feedback && <div className="research-guide-feedback"><Icon name="shield" size={13}/><span>{feedback}</span></div>}
      </main> : <main className="workflow-editor">
        <div className="workflow-toolbar"><div className="workflow-toolbar-title"><button type="button" title="返回研究方案" aria-label="返回研究方案" onClick={openPlanHome}><Icon name="back" size={14}/></button><span><b>{detail?.workflow.name ?? guideDefinition?.name ?? "选择研究方案"}</b><small>{detail ? `我的方案 v${detail.workflow.version} · 用它创建一条新的独立任务` : selectedTemplateId ? "方案模板 · 当前仅预览，不会运行" : "先选择模板或已有方案"}</small></span></div><div className="workflow-view-tabs"><button type="button" className={view === "guide" ? "selected" : ""} onClick={() => setView("guide")}>{detail ? "创建任务" : "方案预览"}</button><button type="button" className={view === "advanced" ? "selected" : ""} onClick={() => setView("advanced")}>编辑方案</button></div>{detail && view === "advanced" && <label>历史版本<select disabled={Boolean(busy)} value={selectedVersionId || detail.workflow.currentVersionId} onChange={(event) => loadVersion(event.target.value)}>{detail.versions.map((version) => <option key={version.id} value={version.id}>v{version.version} · {new Date(version.createdAt).toLocaleString()}</option>)}</select></label>}</div>
        {view === "guide" ? <div className="research-guide" ref={guideRef}>
          {guideDefinition ? <>
            <section className="research-guide-overview"><div><p>YOUR RESEARCH JOURNEY</p><h2>{guideDefinition.name}</h2><span>{guideDefinition.description || "按阶段推进研究，并为关键结果保留来源记录。"}</span></div><aside><b>{guideDefinition.nodes.length}</b><small>个研究阶段</small></aside></section>
            {!detail && selectedTemplateId && <div className="research-template-notice"><Icon name="check" size={16}/><span><b>正在预览模板“{guideDefinition.name}”</b><small>模板不会直接运行。先保存到“我的方案”，然后填写本次任务输入。</small></span></div>}
            <section className="research-stage-map"><header><div><b>研究将如何进行</b><small>AI 与工具默认自主推进；只在证据选择、环境变化和高影响决定处邀请你参与。</small></div><span>开始前预览</span></header><div>{guideDefinition.nodes.map((node, index) => { const interactive = ["human_confirmation", "candidate_selection", "citation_selection"].includes(node.kind) || node.kind === "agent_stage" && node.reviewPolicy !== "auto"; const autonomousAI = node.kind === "agent_stage" && node.reviewPolicy === "auto"; return <article key={node.id} className={index === 0 ? "next" : "pending"}><i>{index + 1}</i><span><b>{node.name}</b><small>{node.prompt || (interactive ? "这一步需要你查看结果并做出选择。" : node.kind === "python" ? "SciAide 将在项目 Python 环境中执行可复现分析。" : "SciAide 自动执行，并保存本阶段的输入与结果。")}</small></span><em>{interactive ? "需要参与" : autonomousAI ? node.skillRouting ? "AI 自主 · Skill" : "AI 自主" : "自动进行"}</em></article>; })}</div></section>
            {startPanel}
          </> : <div className="workflow-run-empty"><Icon name="history" size={27}/><b>请选择一种研究方案</b><span>从左侧选择你想完成的成果类型，完整研究步骤会显示在这里。</span></div>}
          {feedback && <div className="research-guide-feedback"><Icon name="shield" size={13}/><span>{feedback}</span></div>}
        </div> : view === "advanced" ? <>
          <div className="workflow-definition"><label>Workflow JSON<textarea spellCheck={false} value={editor} onChange={(event) => { setSelectedTemplateDefinition(null); setEditor(event.target.value); setPreview(null); setFeedback(""); }} /></label></div>
          <section className="workflow-preview">
            <header><div><b>执行前预览</b><small>{preview ? `${preview.nodes.length} 节点 · ${preview.edgeCount} 边 · ${preview.valid ? "可保存" : "不可保存"}` : "校验后显示冻结工具、顺序、权限与风险"}</small></div><span className={preview?.valid ? "valid" : preview ? "invalid" : "idle"}>{preview?.valid ? "通过" : preview ? "有错误" : "未校验"}</span></header>
            {preview?.diagnostics.length ? <div className="workflow-diagnostics">{preview.diagnostics.map((item, index) => <div className={item.severity} key={`${item.code}:${item.path}:${index}`}><code>{item.code}</code><span><b>{item.path}</b>{item.message}</span></div>)}</div> : preview?.nodes.length ? <div className="workflow-order">{preview.nodes.map((node) => <article key={node.id}><i>{node.ordinal}</i><div><b>{node.name}</b><code>{node.toolName ? `${node.toolName}@${node.toolVersion}` : node.kind}</code><small>{node.summary}</small></div><span className={`risk-${node.risk || "none"}`}>{node.risk || (node.kind === "human_confirmation" ? "人工确认" : "只读选择")}</span><footer>{node.permissions.length ? node.permissions.map((permission) => <em key={`${permission.kind}:${permission.resource}`}>{permission.kind}{permission.resource ? ` · ${permission.resource}` : ""}</em>) : <em>无工具权限</em>}<em>{node.idempotent ? "幂等" : node.sideEffect ? "可能有副作用" : "控制节点"}</em></footer></article>)}</div> : <div className="workflow-preview-empty"><Icon name="shield" size={21}/><span>模板、导入定义和 JSON 都是不可信数据；校验不会执行任何节点。</span></div>}
          </section>
          <footer className="workflow-actions"><span>{feedback}</span><button type="button" onClick={() => void validate()} disabled={Boolean(busy)}><Icon name="shield" size={14}/>{busy === "validate" ? "校验中…" : "静态校验"}</button><button type="button" className="primary" onClick={() => void save()} disabled={Boolean(busy) || preview?.valid === false}><Icon name="check" size={14}/>{busy === "save" ? "保存中…" : detail ? "保存新版本" : "创建流程"}</button></footer>
        </> : null}
      </main>}
    </div>
  </section>;
}
