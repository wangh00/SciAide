import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { createLatestRequestGate, skillTreePrefix } from "./skillSourceTree.js";

test("renders stable prefixes for deep and large source trees", () => {
  const entries = [
    { path: "SKILL.md", kind: "file" },
    { path: "references", kind: "directory" },
    ...Array.from({ length: 50 }, (_, index) => ({ path: `references/group-${String(index).padStart(2, "0")}/file.md`, kind: "file" })),
    { path: "scripts", kind: "directory" },
    { path: "scripts/deep", kind: "directory" },
    { path: "scripts/deep/run.py", kind: "file" },
  ];
  assert.equal(skillTreePrefix(entries, 0), "├── ");
  assert.match(skillTreePrefix(entries, 2), /^│   /);
  assert.equal(skillTreePrefix(entries, entries.length - 1), "        └── ");
  assert.equal(skillTreePrefix(entries, -1), "");
});

test("source browser has independent overflow and a small-screen stacked layout", async () => {
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(css, /\.skill-source-tree\s*\{[^}]*overflow:\s*auto/s);
  assert.match(css, /\.skill-source-viewer pre\s*\{[^}]*overflow:\s*auto/s);
  assert.match(css, /@media \(max-width:\s*720px\)[\s\S]*?\.skill-source-browser\s*\{[^}]*grid-template-columns:\s*1fr/s);
});

test("rapid source switching rejects a late response from the previous request", () => {
  const gate = createLatestRequestGate();
  const slowRequest = gate.begin();
  const latestRequest = gate.begin();
  assert.equal(gate.isCurrent(slowRequest), false);
  assert.equal(gate.isCurrent(latestRequest), true);
  gate.invalidate();
  assert.equal(gate.isCurrent(latestRequest), false);
});
