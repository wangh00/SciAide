export type RuntimeActivity = {runId: string; phase: string; turnIndex: number; startedAt: string};
export function runtimeActivityForRun(value: unknown, run: {id: string; status: string; modelTurns?: number}): RuntimeActivity | null;
export function runtimeActivityLabel(value: unknown, run: {id: string; status: string; modelTurns?: number}, now?: number): string;
