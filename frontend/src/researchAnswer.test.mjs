import test from "node:test";
import assert from "node:assert/strict";
import { researchAnswerPresentation } from "./researchAnswer.js";

const marker = "[K-0191CF709EC7]";
const raw = `Corrected the quote ${marker}.\n\`\`\`json\n${JSON.stringify({ summary: `Evidence is limited ${marker}.`, recommendedReferences: [marker] })}\n\`\`\``;

test("research answer shows the structured result instead of correction narration", () => {
  const view = researchAnswerPresentation(raw, [{ reference: marker }]);
  assert.equal(view.text, `Evidence is limited ${marker}.`);
  assert.equal(view.details, raw);
});

test("unbound markers stay in the raw audit, never pretend to be clickable", () => {
  const view = researchAnswerPresentation(raw);
  assert.equal(view.text, "Evidence is limited .");
  assert.ok(view.details.includes(marker));
});

test("incomplete JSON and non-summary output do not expose internal submission chatter", () => {
  for (const text of ["Corrected.\n```json\n{", "Corrected.\n```json\n{}\n```", "```json\n[]\n```", "```json\ninvalid\n```"] ) {
    assert.deepEqual(researchAnswerPresentation(text), { text: "", details: text });
  }
});

test("plain answers, CRLF output and report claims remain readable", () => {
  assert.deepEqual(researchAnswerPresentation("A plain result."), { text: "A plain result.", details: "" });
  assert.equal(researchAnswerPresentation(raw.replaceAll("\n", "\r\n"), [{ reference: marker }]).text, `Evidence is limited ${marker}.`);
  assert.equal(researchAnswerPresentation('```json\n{"claimSummary":["One","Two"]}\n```').text, "One\n\nTwo");
  assert.equal(researchAnswerPresentation('{"summary":"Unfenced result."}').text, "Unfenced result.");
  assert.equal(researchAnswerPresentation('```json\n{"metadata":{"methodSummary":"Method"},"code":"print(1)"}\n```').text, "Method");
});
