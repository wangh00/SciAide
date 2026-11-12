export function researchSourceSelection(catalog, current = []) {
 const available = new Set(catalog.map(source => source.id));
 const retained = current.filter(id => available.has(id));
 return retained.length ? retained : catalog.filter(source => source.capabilities?.explicitSelection !== true).map(source => source.id);
}

export function sourceQueryProjectionView(source) {
 if (source.queryProjectionVersion !== "source-query-v1") return null;
 const labels = {
  boolean_logic_not_enforced: "AND／OR 关系未被来源执行。",
  negation_not_enforced: "否定约束未被来源执行，仍须筛选核对。",
  field_scope_not_enforced: "字段限定未得到保留。",
  phrase_matching_not_enforced: "引号短语匹配未得到保留。",
 };
 return {
  logical: source.logicalQuery || "",
  input: source.inputQuery || "",
  query: source.providerQuery || "",
  origin: source.queryOrigin === "planned_provider_query" ? "规划中的来源表达式" : "原始检索式",
  mode: ({native_query:"来源原生语法", ranked_bibliographic:"相关性排序关键词", arxiv_boolean:"arXiv 布尔查询"})[source.queryMode] || source.queryMode || "未记录",
  limitations: (source.queryLimitations || []).map(code => labels[code] || `未识别的查询限制：${code}`),
  statement: [source.queryMode === "ranked_bibliographic" ? "仅用于排序发现，不等价于布尔检索；更多关键词不保证找到更多相关文献。" : "", source.queryOrigin === "planned_provider_query" ? "规划式与逻辑原式的语义一致性仍须核对；此处只列语法投影可识别的限制。" : ""].filter(Boolean).join(" "),
  years: source.publicationYears && (source.publicationYears.from || source.publicationYears.to) ? `出版年：${source.publicationYears.from || "不限"}–${source.publicationYears.to || "不限"}（${source.yearFilterApplied ? "拟应用" : "未构造过滤"}）` : "",
  error: source.errorCode === "invalid_query" ? "检索式无法安全转换，未向该来源发送请求；请修改检索式。" : "",
 };
}
