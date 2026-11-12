export function workflowStepCanRetry(run: {status: string; currentStep: number; compilation: {nodes: Array<{id: string; kind: string}>}}, step: {id?: string; attempt?: number; status: string; ordinal: number; nodeId: string; nodeKind: string}, recovery?: {stepId: string; attempt: number} | null): boolean;
export function mergeResearchTimeline<T extends {id: string; sequence: string}>(current: T[], incoming: T[]): T[];
export function compareRunCreatedAt(left: string, right: string): number;
export function orderTimelineDialogue<T extends {message?: {role: string; runId?: string}}>(entries: T[]): T[];

export function workflowReviewGateRejected(run: {compilation: {nodes: Array<{id: string; tool?: {qualifiedName: string}}>}}, step?: {status: string; nodeId: string; errorCode?: string; errorMessage?: string}): boolean;
