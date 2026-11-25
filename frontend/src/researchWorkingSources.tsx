export const workingSourceLabels = {currentFocus:"当前关注",rationale:"工作理由",nextStep:"下一步建议",unknowns:"待核实"} as const;
export type WorkingSourceField = keyof typeof workingSourceLabels;
export type WorkingFieldSource = {
  field: WorkingSourceField;
  status: "recorded" | "not_recorded" | "unavailable";
  stateId?: string;
  version?: number;
  sourceChatRunId?: string;
  sourceUserMessageId?: string;
  sourceCallId?: string;
  createdAt?: string;
  basis?: WorkingBasis[];
};
export type WorkingStateProvenance = {
  projectId: string;
  researchTaskId: string;
  stateId: string;
  version: number;
  status: "unverified_submission_provenance";
  fields: WorkingFieldSource[];
};
export type WorkingSourceView = {phase:"idle"|"loading"|"error"} | {phase:"ready";value:WorkingStateProvenance};
type SourceScope = {projectId:string;taskId:string;stateId:string;version:number};

export function validWorkingSources(value: WorkingStateProvenance, scope: SourceScope): boolean {
  if (!value || value.projectId !== scope.projectId || value.researchTaskId !== scope.taskId || value.stateId !== scope.stateId || value.version !== scope.version || value.status !== "unverified_submission_provenance" || !Array.isArray(value.fields) || value.fields.length !== 4) return false;
  const seen = new Set<string>();
  for (const source of value.fields) {
    if (!source || !Object.hasOwn(workingSourceLabels,source.field) || seen.has(source.field) || !["recorded","not_recorded","unavailable"].includes(source.status)) return false;
    seen.add(source.field);
    if (source.status === "recorded" && (!Number.isSafeInteger(source.version) || source.version! < 1 || source.version! > scope.version || ![source.stateId,source.sourceChatRunId,source.sourceUserMessageId,source.sourceCallId].every(id=>typeof id === "string" && id.trim().length > 0))) return false;
    if (source.status === "recorded" && (source.version === scope.version) !== (source.stateId === scope.stateId)) return false;
    if (source.status !== "recorded" && (source.stateId || source.version || source.sourceCallId || source.sourceChatRunId || source.sourceUserMessageId)) return false;
    if (source.basis !== undefined && (!Array.isArray(source.basis) || source.basis.length>2 || source.status!=="recorded" || source.basis.some(b=>!validWorkingBasis(b)) || new Set(source.basis.map(b=>b.readCallId)).size!==source.basis.length)) return false;
  }
  return true;
}

// One immutable note version, loaded only when requested. No polling, model
// call, source fetch, scientific decision or fallback to a different version.
export function createWorkingSourceReader(options: SourceScope & {
  service:<T>(facade:string,method:string,...args:unknown[])=>Promise<T>;
  onChange:(view:WorkingSourceView)=>void;
}) {
  let disposed=false, inFlight=false, loaded=false;
  async function load() {
    if (disposed || inFlight || loaded || !options.projectId || !options.taskId || !options.stateId || !Number.isSafeInteger(options.version) || options.version < 1) return;
    inFlight=true;
    options.onChange({phase:"loading"});
    try {
      const value=await options.service<WorkingStateProvenance>("WorkflowFacade","GetResearchWorkingStateProvenance",options.projectId,options.taskId,options.version);
      if (disposed) return;
      if (!validWorkingSources(value,options)) throw new Error("Working-note source scope or version changed");
      loaded=true;
      options.onChange({phase:"ready",value});
    } catch {
      if (!disposed) options.onChange({phase:"error"});
    } finally { inFlight=false; }
  }
  return {load,dispose(){disposed=true;}};
}
import {validWorkingBasis, type WorkingBasis} from "./researchWorkingBasis";
