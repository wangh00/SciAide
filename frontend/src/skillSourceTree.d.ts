export function skillTreePrefix(entries: ReadonlyArray<{ path: string; kind: string }>, index: number): string;
export function createLatestRequestGate(): { begin(): number; isCurrent(token: number): boolean; invalidate(): void };
