export type DisplayCitation = {
  reference: string;
  sourceName: string;
  locator?: string;
  title?: string;
  quote: string;
  quoteSha256: string;
  evidenceLevel?: string;
  bibliography?: { data?: { title?: string; doi?: string; url?: string; year?: number; containerTitle?: string } };
};
export type CitationDisplayMap = Map<string, { value: DisplayCitation; number: number }>;
export function reportCitationSnapshot(outputs: unknown): unknown[];
export function citationDisplayMap(values: unknown): CitationDisplayMap;
export function citationTextSegments(text: string): { text: string; reference: string }[];
export function citationSourceURL(citation: DisplayCitation): string;
