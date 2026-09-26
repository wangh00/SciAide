const labelByBuiltinName = {
  "builtin.resource.open": "操作任务资源",
  "builtin.research.task.read": "查看任务记录",
  "builtin.research.revision.propose": "拟定返修方案",
  "builtin.resource.search": "检索任务资料",
  "builtin.attachment.list": "列出项目附件",
  "builtin.document.inspect": "检查文档结构",
  "builtin.document.read": "读取文档内容",
  "builtin.document.search": "搜索文档内容",
  "builtin.knowledge.search": "本地知识库检索",
  "builtin.research.catalog": "查看文献来源",
  "builtin.research.search": "在线学术检索",
  "builtin.web.search": "搜索互联网",
  "builtin.web.open": "读取网页",
  "builtin.browser.open": "浏览器访问网页",
  "builtin.research.fetch": "获取文献记录",
  "builtin.workspace.list": "浏览 Workspace",
  "builtin.workspace.read_text": "读取 Workspace 文件",
  "builtin.shell.execute": "执行 Shell",
  "builtin.python.execute": "执行 Python",
  "builtin.python.kernel.execute": "运行 Python 分析",
  "builtin.python.kernel.manage": "管理 Python Kernel",
  "builtin.python.environment.install": "安装 Python 依赖",
  "builtin.research.workflow.search": "执行科研文献检索",
  "builtin.research.workflow.import": "导入科研资料",
  "builtin.research.workflow.sync": "同步项目知识库",
  "builtin.research.workflow.python.ensure": "检查项目 Python 环境",
  "builtin.research.workflow.python.prepare": "准备 Python 分析环境",
  "builtin.research.workflow.report": "发布科研报告",
  "builtin.research.workflow.review.gate": "核验科研交付条件",
};

const preferredArgumentKeys = [
  "name", "category", "resourcePath", "path", "attachmentId", "locator", "query", "url", "action",
  "section", "offset", "limit", "workdir", "timeoutSeconds", "scriptPath", "command", "code",
];

function record(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

const sensitiveArgumentKey = /(api[_-]?key|secret|token|password|passwd|credential|authorization|cookie|private[_-]?key|client[_-]?secret|access[_-]?key)/i;
const bearerValue = /(\bBearer\s+)[A-Za-z0-9._~+/=-]+/gi;
const headerSecretValue = /(\b(?:authorization|proxy-authorization|x-api-key|api-key|token|secret|password)\s*[:=]\s*)[^\s,;]+/gi;
const querySecretValue = /([?&](?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret|password)=)[^&#\s]+/gi;

function redactArgumentString(value) {
  return value.replace(bearerValue, "$1[已隐藏]").replace(headerSecretValue, "$1[已隐藏]").replace(querySecretValue, "$1[已隐藏]");
}

function projectArgumentValue(value, key = "", depth = 0) {
  if (depth >= 8) return "…（嵌套内容已省略）";
  if (sensitiveArgumentKey.test(String(key))) return "[已隐藏]";
  if (typeof value === "string") {
    const normalized = redactArgumentString(value.trim());
    return normalized.length > 1200 ? `${normalized.slice(0, 1200)}…（已截断）` : normalized;
  }
  if (Array.isArray(value)) {
    return value.slice(0, 32).map((item) => projectArgumentValue(item, key, depth + 1)).concat(value.length > 32 ? ["…（其余项目已省略）"] : []);
  }
  if (value && typeof value === "object") {
    const entries = Object.entries(value);
    const result = {};
    entries.slice(0, 64).forEach(([childKey, child]) => { result[childKey] = projectArgumentValue(child, childKey, depth + 1); });
    if (entries.length > 64) result["…（其余参数已省略）"] = true;
    return result;
  }
  return value;
}

// Frontend defense-in-depth for ordinary chat snapshots. Workflow projections
// are already sanitized by the backend, but applying the same bounded view
// here keeps one rendering contract for both sources.
export function safeToolArguments(value) {
  if (value === undefined || value === null) return undefined;
  const projected = projectArgumentValue(value);
  try {
    const encoded = JSON.stringify(projected);
    if (encoded && encoded.length > 16 * 1024) return { details: "参数过大，已省略" };
  } catch {
    return undefined;
  }
  return projected;
}

function compactText(value, limit = 150) {
  const text = String(value ?? "").replace(/\s+/g, " ").trim();
  return text.length > limit ? `${text.slice(0, Math.max(1, limit - 1))}…` : text;
}

function valueText(key, value) {
  if (typeof value === "string") {
    const prefix = key === "timeoutSeconds" ? "" : key === "limit" ? "最多 " : "";
    const suffix = key === "timeoutSeconds" ? " 秒" : key === "limit" ? " 字符" : "";
    return `${prefix}${compactText(value, key === "command" || key === "code" ? 120 : 90)}${suffix}`;
  }
  if (typeof value === "number" || typeof value === "boolean") {
    if (key === "timeoutSeconds") return `${value} 秒`;
    if (key === "limit") return `最多 ${value.toLocaleString()} 字符`;
    if (key === "offset") return `偏移 ${value.toLocaleString()}`;
    return String(value);
  }
  if (Array.isArray(value)) return value.slice(0, 3).map((item) => compactText(item, 42)).join("、");
  return "";
}

function summarizeArguments(argumentsValue, excluded = []) {
  const args = record(argumentsValue);
  const ignored = new Set(excluded);
  const parts = [];
  for (const key of preferredArgumentKeys) {
    if (ignored.has(key) || args[key] === undefined || args[key] === "" || args[key] === 0) continue;
    const text = valueText(key, args[key]);
    if (text && !parts.includes(text)) parts.push(text);
    if (parts.length === 2) break;
  }
  return parts.join(" · ");
}

function humanizeToolName(name) {
  const value = String(name ?? "").split(".").pop() || "tool";
  return value.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}

// Skill calls already identify the Skill and operation in their title. The
// qualified implementation name and sub-100ms duration add noise without
// helping the user understand the research progress.
export function isSkillToolName(name) {
  const value = String(name ?? "");
  return value === "builtin.skill.load" || value.startsWith("builtin.skill.resource.");
}

export function activityTruncationLabel(name) {
  return isSkillToolName(name) ? "内容较长 · 分段读取" : "输出较长 · 已省略部分";
}

export function activityDurationLabel(name, durationMillis) {
  if (isSkillToolName(name)) return "";
  const millis = Number(durationMillis);
  if (!Number.isFinite(millis) || millis <= 0) return "";
  const tenths = Math.round(millis / 100);
  return tenths > 0 ? `${tenths / 10} 秒` : "";
}

export function toolPresentation(call) {
  const name = String(call?.toolName ?? "");
  const args = record(call?.arguments);
  if (name === "builtin.mcp.list" || name === "builtin.tools.search") {
    return { kind: "MCP", icon: "server", title: name === "builtin.mcp.list" ? "查看 MCP 状态" : "查找 MCP 工具", summary: compactText(args.query || args.server || "", 100) };
  }
  if (name === "builtin.workspace.list") {
    const path = compactText(args.path || ".", 90);
    const limit = Number(args.limit);
    return { kind: "工具", icon: "tool", title: labelByBuiltinName[name], summary: path + (Number.isFinite(limit) && limit > 0 ? ` · 最多 ${limit.toLocaleString()} 项` : "") };
  }
  if (name === "builtin.resource.open" || name === "builtin.resource.search") {
    const resolved = record(call?.result?.structured);
    const skill = String(resolved.sourceTool ?? "").startsWith("builtin.skill.");
    const label = compactText(redactArgumentString(String(resolved.label ?? "")), 120);
    return { kind: skill ? "Skill" : "工具", icon: skill ? "skill" : "tool", title: label || labelByBuiltinName[name], summary: compactText(redactArgumentString(String(args.query ?? "")), 100) };
  }
  if (name === "builtin.workflow.citation.seed") {
    return { kind: "工具", icon: "tool", title: "Seed", summary: "系统正在把已核验的科研证据“注入”当前 AI 对话" };
  }
  if (name === "builtin.skill.load") {
    const target = compactText(args.name || args.category || "Skill 目录", 80);
    const action = args.name ? "加载 Skill" : args.category ? "浏览 Skill 分类" : "浏览 Skill 目录";
    return { kind: "Skill", icon: "skill", title: `${action} · ${target}`, summary: summarizeArguments(args, ["name", "category"]) };
  }
  if (name.startsWith("builtin.skill.resource.")) {
    const action = name.endsWith(".list") ? "列出 Skill 资源" : name.endsWith(".materialize") ? "发布 Skill 资源" : "读取 Skill 资源";
    const target = compactText(args.name || "Skill", 60);
    const resource = compactText(args.resourcePath || args.section || "", 90);
    return { kind: "Skill", icon: "skill", title: `${action} · ${target}`, summary: resource || summarizeArguments(args, ["name"]) };
  }
  if (name.startsWith("mcp.")) {
    const parts = name.split(".");
    const namespace = parts[1] || "MCP";
    const nativeName = parts.slice(2).join(".") || "tool";
    return { kind: "MCP", icon: "server", title: `${namespace} · ${nativeName}`, summary: summarizeArguments(args) };
  }
  const title = labelByBuiltinName[name] || humanizeToolName(name);
  return { kind: "工具", icon: name.includes("python") ? "chart" : "tool", title, summary: summarizeArguments(args) };
}

export function toolResultSummary(call) {
  const text = compactText(call?.result?.text || call?.errorMessage || "", 180)
    .replace(/^(?:#{1,6}|[-*])\s+/, "")
    .replace(/^```\w*\s*/, "");
  if (text) return text;
  const status = call?.result?.status || call?.status;
  if (status === "failed" || status === "error") return "工具执行失败";
  if (status === "denied") return "调用已拒绝";
  if (status === "cancelled" || status === "interrupted") return "调用已中止";
  return "";
}
// Task discussion tools do not execute research stages.
