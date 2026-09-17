import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");

test("literature progress distinguishes screening, synthesis and source readback", () => {
  assert.match(source, /output\.phase === "synthesis"/);
  assert.match(source, /progress\.readback[\s\S]*?回查原始文献材料/);
  assert.match(source, /progress\.level \|\| 1/);
  assert.match(source, /<WorkflowHumanDecision projectId=\{detail\.run\.projectId\}/);
});

test("literature original abstracts are read lazily and stale reads are ignored", () => {
  const component = source.slice(source.indexOf("function LiteratureSourceAbstract"), source.indexOf("function WorkflowHumanDecision"));
  assert.match(component, /ReadLiteratureCandidate/);
  assert.match(component, /requests\.current\.isCurrent/);
  assert.match(component, /onToggle=[\s\S]*?currentTarget\.open/);
  assert.doesNotMatch(component, /useEffect\([\s\S]*?=> \{[^}]*backend/);
});
