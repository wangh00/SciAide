export type RouteDataPort = {name: string; type: string; fileKind?: string; maxItems?: number; minItems?: number; required?: boolean};
export type RouteDataCandidate = {attachmentId: string; name: string; sizeBytes: number; sha256: string; portNames: string[]};
export type RouteDataFile = {relativePath: string; name: string; sizeBytes: number; sha256: string; attachmentId: string};
export function routeDataPaths(raw: string | undefined, type: string): string[];
export function routeDataAutofillTarget(ports: RouteDataPort[], candidates: RouteDataCandidate[], values: Record<string, string>): {port: RouteDataPort; candidate: RouteDataCandidate} | null;
export function routeDataDraftWithFile(values: Record<string, string>, port: RouteDataPort, file: {relativePath: string}): Record<string, string>;

export function routeDataOmitEmpty(port: RouteDataPort, value: unknown): boolean;
export function routeDataInputReady(port: RouteDataPort, value: unknown): boolean;
