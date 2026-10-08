export type RepairCitation = {id: string; reference?: string};
export type CitationRepairModel = {previousCount: number; added: number; removed: number; unchanged: boolean; mentioned: Set<string>; missingMentioned: RepairCitation[]; missingRecommended: RepairCitation[]; corrections: string[]; repeatedIssueCount: number};
export function citationRepairModel(step: {nodeKind: string; input?: Record<string, unknown>}, selectedKeys: string[]): CitationRepairModel | null;
export function supplementCitationKeys(selectedKeys: string[], candidates: RepairCitation[]): string[];
