export type RetryDisplayStatus = {phase: string; reason?: string; attempt: number; maxAttempts: number; delayMillis: number; message: string};
export function retryStatusLabel(retry?: RetryDisplayStatus | null): string;
export function toolDecisionLabel(activity?: {errorCode?: string; errorMessage?: string; toolName?: string; status?: string} | null): string;
export function applyRetryEvent(current: Record<string, RetryDisplayStatus>, event: {aggregateId: string; type: string; payload?: Record<string, unknown>}): Record<string, RetryDisplayStatus>;
