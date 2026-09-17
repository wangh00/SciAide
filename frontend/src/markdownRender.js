const citationTextPattern = /\[K-[0-9A-F]{12}\]/g;
const containsCitationTextPattern = /\[K-[0-9A-F]{12}\]/;
const citationURLPattern = /^sciaide-citation:(K-[0-9A-F]{12})$/;

function citationNodes(value) {
  const nodes = [];
  let start = 0;
  citationTextPattern.lastIndex = 0;
  for (const match of value.matchAll(citationTextPattern)) {
    const index = match.index ?? 0;
    if (index > start) nodes.push({ type: "text", value: value.slice(start, index) });
    const reference = match[0].slice(1, -1);
    nodes.push({
      type: "link",
      url: `sciaide-citation:${reference}`,
      children: [{ type: "text", value: match[0] }],
    });
    start = index + match[0].length;
  }
  if (start < value.length) nodes.push({ type: "text", value: value.slice(start) });
  return nodes;
}

function rewriteCitationText(node, protectedContext = false) {
  if (!Array.isArray(node?.children)) return;
  const next = [];
  for (const child of node.children) {
    if (!protectedContext && child?.type === "text" && containsCitationTextPattern.test(child.value ?? "")) {
      next.push(...citationNodes(child.value));
      continue;
    }
    const childProtected = protectedContext || child?.type === "link" || child?.type === "linkReference";
    rewriteCitationText(child, childProtected);
    next.push(child);
  }
  node.children = next;
}

export function remarkSciAideCitations() {
  return (tree) => rewriteCitationText(tree);
}

export function citationReferenceFromURL(value) {
  const match = citationURLPattern.exec((value ?? "").trim());
  return match ? `[${match[1]}]` : "";
}

export function safeMarkdownURL(value) {
  const candidate = (value ?? "").trim();
  if (!candidate) return "";
  if (citationReferenceFromURL(candidate) || candidate.startsWith("#")) return candidate;
  try {
    const parsed = new URL(candidate);
    return ["http:", "https:", "mailto:"].includes(parsed.protocol) ? candidate : "";
  } catch {
    return "";
  }
}
