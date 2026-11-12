import './recordIdentity.css';

export type RecordIdentity = {
  version: string;
  scopes?: {roleCount: number; relationCount: number; omittedRoles: number; omittedRelations: number; roleKinds?: Record<string, number>; relationKinds?: Record<string, number>}[];
  roles?: {kind: string; field: string; record: {sourceId: string; sourceRecordId: string; workSha256: string}}[];
  relations?: {kind: string; targetCandidateId: string; records: {sourceId: string; sourceRecordId: string; workSha256: string}[]; basis: string[]}[];
  preview?: {sha256: string; roleCount: number; relationCount: number; truncated: boolean; scopeCount?: number};
};

const labels: Record<string, string> = {
  companion_material: '补充/伴随材料线索', dataset: '来源标记为数据集', protocol: '来源标记为研究方案',
  thesis: '来源标记为学位论文', preprint_or_working_paper: '来源标记为预印本/工作论文',
  possible_same_work: '疑似同一成果', possible_version: '疑似不同版本', possible_companion: '疑似主文与伴随材料',
  identifier_conflict: '标识冲突待核对', shared_identifier: '共用标识但仍为不同候选',
};

export function RecordIdentityHints({identity}: {identity?: RecordIdentity}) {
  if (!identity) return null;
  if (identity.version !== 'record-identity-hints-v1') return <span className="record-identity-hints">线索版本未知，原题录保留；未认定研究身份。</span>;
  const roles = identity.roles ?? [], relations = identity.relations ?? [];
  const scopes = identity.scopes ?? [];
  const roleCount = identity.preview?.roleCount ?? (scopes.length ? scopes.reduce((n, s) => n + s.roleCount, 0) : roles.length);
  const relationCount = identity.preview?.relationCount ?? (scopes.length ? scopes.reduce((n, s) => n + s.relationCount, 0) : relations.length);
  const omitted = identity.preview?.truncated || roles.length > 2 || relations.length > 2 || scopes.some(s => s.omittedRoles > 0 || s.omittedRelations > 0);
  if (!roles.length && !relations.length) return null;
  return <span className="record-identity-hints">
    <strong>题录身份线索 · 待核实</strong>
    <span role="list">{roles.slice(0, 2).map((hint, i) => <span role="listitem" key={`role-${i}`}>{labels[hint.kind] ?? hint.kind} · {hint.record.sourceId}:{hint.record.sourceRecordId}（{hint.field}）</span>)}
      {relations.slice(0, 2).map((hint, i) => <span role="listitem" key={`relation-${i}`}>{labels[hint.kind] ?? hint.kind} · {hint.records.map(ref => `${ref.sourceId}:${ref.sourceRecordId}`).join(' ↔ ')} · 目标候选 {hint.targetCandidateId}</span>)}
    </span>
    <small>未自动合并或排除；文档数不等于独立研究数。没有提示也不证明独立。</small>
    {omitted && <small title={identity.preview ? `有界清单内容 SHA256 ${identity.preview.sha256}（不含本地候选 ID）` : undefined}>视图有省略：检测到 {roleCount} 条类型、{relationCount} 条关系线索；只保存有限见证及计数，原题录保留。多查询条目不跨范围去重。</small>}
  </span>;
}
