export function researchSourceSelection(catalog: { id: string; capabilities?: { explicitSelection?: boolean } }[], current?: string[]): string[];

export type SourceQueryAudit = { logicalQuery?: string; inputQuery?: string; effectiveQuery?: string; providerQuery?: string; queryMode?: string; queryOrigin?: string; queryProjectionVersion?: string; queryLimitations?: string[]; errorCode?: string; publicationYears?: {from: number; to: number}; yearFilterApplied?: boolean };
export function sourceQueryProjectionView(source: SourceQueryAudit): { logical: string; input: string; query: string; origin: string; mode: string; limitations: string[]; statement: string; error: string; years: string } | null;
