export type RetryDisplayStatus = {phase: string; attempt: number; maxAttempts: number; delayMillis: number; message: string};
export function retryStatusLabel(retry?: RetryDisplayStatus | null): string;
export function applyRetryEvent(current: Record<string, RetryDisplayStatus>, event: {aggregateId: string; type: string; payload?: Record<string, unknown>}): Record<string, RetryDisplayStatus>;
