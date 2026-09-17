/** Read-only presentation of review evidence. Never changes approval or raw issues. */
export function reviewTrackingModel(output) {
  const findings = Array.isArray(output?.reviewFindings) ? output.reviewFindings.filter(item => item && typeof item === 'object'
    && ['id', 'summary', 'location', 'reason', 'origin', 'changeReason'].every(key => typeof item[key] === 'string')
    && ['open', 'resolved', 'withdrawn'].includes(item.status)).map(item => ({ id: item.id, status: item.status, summary: item.summary, location: item.location, reason: item.reason, origin: item.origin, changeReason: item.changeReason })) : [];
  const suggestions = Array.isArray(output?.suggestions) ? output.suggestions.filter(item => typeof item === 'string' && item.trim()) : [];
  return { findings, suggestions };
}

export function readableReviewText(value) {
  let text = String(value ?? '').replace(/&#(?:x([0-9a-f]+)|(\d+));/gi, (raw, hex, dec) => {
    const code = Number.parseInt(hex || dec, hex ? 16 : 10);
    return code > 0 && code <= 0x10ffff && !(code >= 0xd800 && code <= 0xdfff) ? String.fromCodePoint(code) : raw;
  }).replace(/&nbsp;/gi, ' ').replace(/&amp;/gi, '&').trim();
  const terms = [
    [/researchContract\.methodComponents/gi, '原研究计划中的分析方法'],
    [/claimSummary/gi, '报告中的结论摘要'], [/computed_results/gi, '实际计算结果'],
    [/report_draft|delivery_draft/gi, '报告初稿'], [/reviewedInputSha256/gi, '本次审查所对应的结果标识'],
    [/method_implementation/gi, '分析方法实现'], [/result_interpretation/gi, '结果解释'],
    [/peer[-_ ]review|independent_review/gi, '结果复核'], [/delivery[_ ]gate/gi, '交付前检查'],
    [/\bworkflow\b/gi, '任务流程'], [/\bworkspace\b/gi, '项目文件夹'],
    [/\bmean_diff\b/gi, '两组平均值之差'], [/\bgroup1\b/gi, '第一组'], [/\bgroup2\b/gi, '第二组'],
    [/\bANCOVA\b/g, '考虑初始差异后的组间比较'], [/\bTukey HSD\b/gi, '各组之间的两两比较'],
    [/\bOLS\b/g, '最小二乘拟合'], [/\bCI\b/g, '置信区间（估计结果的不确定范围）'],
    [/\bSchema\b/gi, '结果格式要求'], [/\bJSON\b/gi, '程序保存的结构化记录'],
  ];
  for (const [pattern, replacement] of terms) text = text.replace(pattern, replacement);
  return text;
}

const categories = {
  unsupportedClaims: '核对结论是否超出了现有资料或数据能够支持的范围。',
  citationIssues: '核对引用是否可追溯，以及原文是否真的支持报告中的说法。',
  numericIssues: '核对正文、图表与实际计算记录是否一致。',
  methodIssues: '检查所用方法的前提、实施过程和限制是否交代清楚。',
  requiredCorrections: '按审查意见补齐内容，完成后再检查是否满足交付要求。',
};

export function presentReviewIssue(raw, key) {
  const text = readableReviewText(raw);
  // Use narrow, action-oriented explanations, not an invented new diagnosis.
  if (/mean_diff|Tukey HSD/i.test(raw) && /方向|符号|减|group[12]|mean\(group/i.test(raw)) {
    return { title: '把两组差异的计算方向说明清楚', explanation: '明确是哪一组减去哪一组，并核对正文、表格和图注，避免把“谁更高、谁更低”读反。', raw };
  }
  if (/ANCOVA/i.test(raw) && /交互|斜率|interaction/i.test(raw)) {
    return { title: '补查各组是否适合使用同一种调整方法', explanation: '检查初始状态对最终结果的影响在各组是否一致；如果样本或设计不允许检查，应说明原因和结论限制。', raw };
  }
  if (/(?:CI|置信区间)/i.test(raw) && /非中心|近似|潜在偏差|计算方法|计算正确/i.test(raw)) {
    return { title: '核对统计估计的可靠范围', explanation: '检查不确定范围的计算方法是否合适，并说明有效样本量、近似处理及其可能的影响。', raw };
  }
  if (/researchContract\.methodComponents|Michaelis.Menten|指数渐近/i.test(raw) && /未|没有|仅|只|说明/i.test(raw)) {
    return { title: '说明原计划中哪些分析没有完成', explanation: '对照原研究计划，补做缺少的分析，或明确说明为什么不适合做，以及这会限制哪些结论。', raw };
  }
  if (/报告文件|report\.(?:md|pdf)|报告.*文件|文件.*报告/i.test(raw) && /统一|缺少|缺失|创建|整理|尚未|未生成|未提供/i.test(raw)) {
    return { title: '整理一份可以直接阅读的完整报告', explanation: '把研究问题、方法、结果、图表和局限放进同一份报告，而不是让读者自行拼接多个结果文件。', raw };
  }
  if (/peer[-_ ]review|delivery[_ ]gate/i.test(raw) && /角色|身份|配置|步骤|阶段/i.test(raw)) {
    return { title: '说明结果是如何复核的', explanation: '清楚区分生成结果与复核结果的步骤，并保留复核记录和是否通过的结论。', raw };
  }
  const guidance = categories[key] || categories.requiredCorrections;
  const technical = /\b[A-Za-z]+(?:_[A-Za-z0-9]+)+\b|\b[A-Za-z]+\.[A-Za-z]+\b|[A-Za-z]{12,}|[{}]/.test(text);
  return { title: '', explanation: technical ? guidance : text || guidance, raw };
}

export function reviewFeedbackModel(issues) {
  const groups = issues?.groups || [];
  const corrections = groups.find(group => group.key === 'requiredCorrections');
  const categorized = groups.filter(group => group.key !== 'requiredCorrections');
  // Corrections summarize the category findings; don't count them twice.
  const shown = corrections?.items.length ? [corrections] : categorized;
  const items = shown.flatMap(group => group.items.map(raw => presentReviewIssue(raw, group.key)));
  const classifiedCount = categorized.reduce((count, group) => count + group.items.length, 0);
  return {
    title: '报告还需要修改，暂未交付',
    summary: corrections?.items.length
      ? `本次复核要求完成 ${corrections.items.length} 项修改。修改并重新通过检查后，才能交付结果。`
      : items.length ? `本次复核发现 ${items.length} 项待处理问题，当前结果暂不能交付。`
      : '目前还无法确认报告已满足交付要求，需要重新检查。',
    items, classifiedCount,
    correctionCount: corrections?.items.length || 0,
  };
}
