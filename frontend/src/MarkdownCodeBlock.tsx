import { Children, isValidElement, ReactNode, useEffect, useRef, useState } from "react";

function codeText(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(codeText).join("");
  if (isValidElement<{children?: ReactNode}>(node)) return codeText(node.props.children);
  return "";
}

export function MarkdownCodeBlock({ children, copy }: { children: ReactNode; copy: (text: string) => Promise<void> }) {
  const [status, setStatus] = useState<"idle" | "copied" | "error">("idle");
  const timer = useRef<ReturnType<typeof setTimeout>>();
  useEffect(() => () => clearTimeout(timer.current), []);
  const code = Children.toArray(children).find(isValidElement);
  const language = isValidElement<{className?: string}>(code) ? /language-([^\s]+)/.exec(code.props.className ?? "")?.[1] : "";
  async function handleCopy() {
    clearTimeout(timer.current);
    try { await copy(codeText(children)); setStatus("copied"); }
    catch { setStatus("error"); }
    timer.current = setTimeout(() => setStatus("idle"), 2000);
  }
  return <div className="markdown-code-block"><div className="markdown-code-toolbar"><span>{language || "代码"}</span><button type="button" aria-label="复制代码" onClick={() => void handleCopy()}>{status === "copied" ? "已复制" : status === "error" ? "复制失败，重试" : "复制"}</button></div><pre>{children}</pre></div>;
}
