export type MarkdownASTNode = {
  type?: string;
  value?: string;
  url?: string;
  children?: MarkdownASTNode[];
};

export function remarkSciAideCitations(): (tree: MarkdownASTNode) => void;
export function citationReferenceFromURL(value?: string): string;
export function safeMarkdownURL(value?: string): string;
