export type ToolActivityInput = {
  toolName: string;
  arguments?: unknown;
  status?: string;
  errorMessage?: string;
  result?: { status?: string; text?: string; structured?: unknown };
};

export type ToolPresentation = {
  kind: "Skill" | "MCP" | "工具";
  icon: "skill" | "server" | "chart" | "tool";
  title: string;
  summary: string;
};

export function toolPresentation(call: ToolActivityInput): ToolPresentation;
export function toolResultSummary(call: ToolActivityInput): string;
export function safeToolArguments(value: unknown): unknown;
export function isSkillToolName(name: string): boolean;
export function activityTruncationLabel(name: string): string;
export function activityDurationLabel(name: string, durationMillis?: number): string;
