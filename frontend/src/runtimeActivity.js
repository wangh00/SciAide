const phases = new Set(["model_wait", "model_processing", "implementation_wait", "implementation_validation", "compaction", "compaction_completed", "tools"]);

export function runtimeActivityForRun(value, run) {
  if (!value || run?.status !== "running" || value.runId !== run.id
    || !Number.isInteger(value.turnIndex) || value.turnIndex !== run.modelTurns
    || !phases.has(value.phase) || !Number.isFinite(Date.parse(value.startedAt))) return null;
  return value;
}

export function runtimeActivityLabel(value, run, now = Date.now()) {
  const activity = runtimeActivityForRun(value, run);
  if (!activity) return "";
  const seconds = Math.max(0, Math.floor((now - Date.parse(activity.startedAt)) / 1000));
  const duration = seconds < 60 ? `${seconds}秒` : `${Math.floor(seconds / 60)}分${seconds % 60}秒`;
  const labels = {
    model_wait: "等待模型响应", model_processing: "模型已响应，等待完整结果（尚未完成）",
    implementation_wait: "已保存实现片段，正在生成下一段（尚未执行）",
    implementation_validation: "源码已组装，正在核验完整实现（尚未执行）",
    compaction: "正在压缩会话上下文，原始历史保留", compaction_completed: "会话摘要已保存，准备继续",
    tools: "正在处理工具请求",
  };
  return `第${activity.turnIndex}轮 · ${labels[activity.phase]} · ${duration}`;
}
