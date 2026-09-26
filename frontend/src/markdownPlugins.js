import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import rehypeKatex from "rehype-katex";
import { remarkSciAideCitations } from "./markdownRender.js";

// Parse math before citation rewriting. Code and TeX source remain separate AST nodes.
export const markdownRemarkPlugins = [remarkGfm, remarkMath, remarkSciAideCitations];
// Untrusted model output must not enable TeX HTML, remote images or file links.
// Bound macro expansion and dimensions; invalid/incomplete expressions stay readable.
export const markdownRehypePlugins = [[rehypeKatex, {
  trust: false,
  strict: "ignore",
  maxExpand: 200,
  maxSize: 20,
  output: "htmlAndMathml",
}]];
