export type ReviewFinding = { id: string; status: 'open' | 'resolved' | 'withdrawn'; summary: string; location: string; reason: string; origin: string; changeReason: string };
export type ReviewIssues = { approved?: boolean; groups: { key: string; label: string; items: string[] }[]; total: number; findings?: ReviewFinding[]; suggestions?: string[] };
export function reviewTrackingModel(output: Record<string, unknown>): { findings: ReviewFinding[]; suggestions: string[] };
export type ReviewIssuePresentation = { title: string; explanation: string; raw: string };
export function readableReviewText(value: unknown): string;
export function presentReviewIssue(raw: string, key: string): ReviewIssuePresentation;
export function reviewFeedbackModel(issues?: ReviewIssues | null): {title: string; summary: string; items: ReviewIssuePresentation[]; classifiedCount: number; correctionCount: number};
