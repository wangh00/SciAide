import type {ReactNode} from "react";

// Waiting for an answer is still an active ordinary discussion. Its Stop
// control must not disappear merely because the question card is visible.
// Workflow-owned executions retain their separate task cancellation owner.
export function ConversationStopButton({busy, workflowAI, workflowLocked, runId, runConversationId,
  conversationId, sending, answering, cancellingRunId, waitingForAnswer, onStop, icon}: {
  busy: boolean; workflowAI: boolean; workflowLocked: boolean; runId?: string;
  runConversationId?: string; conversationId: string; sending: boolean; answering: boolean;
  cancellingRunId: string; waitingForAnswer: boolean; onStop: () => void; icon: ReactNode;
}) {
  if (!busy || workflowAI || workflowLocked) return null;
  const cancelling = Boolean(runId && cancellingRunId === runId);
  const label = cancelling ? "正在停止" : waitingForAnswer ? "停止讨论" : "停止生成";
  return <button type="button" className="send is-stop" aria-label={label}
    title={cancelling ? "正在停止…" : waitingForAnswer ? "停止当前讨论，不提交或跳过问题，也不再调用模型" : "停止生成"}
    disabled={sending || answering || !runId || runConversationId !== conversationId || cancelling}
    onClick={onStop}>{icon}</button>;
}
