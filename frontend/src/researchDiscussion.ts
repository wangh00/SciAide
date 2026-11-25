export type ResearchDiscussionEntry = {
  projectId: string;
  researchTaskId: string;
  conversationId: string;
  title: string;
  researchQuestion: string;
  modelProfileId: string;
  modelId: string;
  referenceAttachmentIds: string[];
  permissionMode: "plan" | "full_access";
  reasoningLevel: string;
  createdAt: string;
  canExplore: boolean;
  sourceWorkflowRunId?: string;
};

export function normalizeResearchDiscussionEntry(value: ResearchDiscussionEntry): ResearchDiscussionEntry {
  if (!value || typeof value.projectId !== "string" || typeof value.researchTaskId !== "string" || typeof value.conversationId !== "string"
    || !value.projectId.trim() || !value.researchTaskId.trim() || !value.conversationId.trim()
    || typeof value.researchQuestion !== "string" || typeof value.title !== "string" || typeof value.canExplore !== "boolean"
    || !Array.isArray(value.referenceAttachmentIds) || value.referenceAttachmentIds.some(id => typeof id !== "string" || !id.trim())) {
    throw new Error("Invalid direct research task identity");
  }
  return {...value, referenceAttachmentIds: [...value.referenceAttachmentIds], sourceWorkflowRunId: typeof value.sourceWorkflowRunId === "string" ? value.sourceWorkflowRunId : ""};
}

export function researchDiscussionEntryAvailable(entry: ResearchDiscussionEntry | null): boolean {
  return Boolean(entry && entry.canExplore === true && entry.projectId.trim() && entry.researchTaskId.trim() && entry.conversationId.trim());
}

export type ResearchDiscussionContext = {
  projectId: string;
  researchTaskId: string;
  conversationId: string;
  primaryConversationId: string;
  sourceWorkflowRunId: string;
  canExplore: boolean;
};

type DiscussionDetail = {
  run: { projectId?: string; researchTaskId?: string; conversationId?: string };
  researchDiscussion?: ResearchDiscussionContext;
};

// This is an explicit host capability, never inferred from planner status or
// purpose. Mismatched, missing and malformed responses fail closed.
export function researchDiscussionAvailable(detail: DiscussionDetail | null): boolean {
  const context = detail?.researchDiscussion;
  return Boolean(context && context.canExplore === true && context.projectId === detail?.run.projectId
    && context.researchTaskId && context.researchTaskId === detail?.run.researchTaskId
    && context.conversationId && context.conversationId === detail?.run.conversationId
    && typeof context.primaryConversationId === "string" && context.primaryConversationId.trim());
}

export function researchDiscussionConversation(detail: DiscussionDetail): string {
  return researchDiscussionAvailable(detail) ? detail.researchDiscussion!.primaryConversationId : detail.run.conversationId ?? "";
}
