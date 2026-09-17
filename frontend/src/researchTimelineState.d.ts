export function mergeResearchTimeline<T extends {id: string; sequence: string}>(current: T[], incoming: T[]): T[];
export function compareRunCreatedAt(left: string, right: string): number;
