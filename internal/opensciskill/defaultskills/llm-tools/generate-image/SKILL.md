---
name: generate-image
description: Disclose that SciAide has no native managed image-generation Tool or wallet route.
category: llm-tools
entry: false
---

> **SciAide execution boundary:** Default Skill files are embedded read-only context, not an executable directory. Before running a bundled script, use `builtin.skill.resource.materialize` to publish the reviewed file into the current Workspace, inspect it, and then invoke `builtin.python.execute` or `builtin.shell.execute` through the normal Tool approval path. Treat relative commands below as examples rooted at the materialized files; do not assume an upstream `skills/` directory exists. Install project dependencies only through `builtin.python.environment.install` with separate approval. SciAide never injects model, MCP, or application secrets into child processes.


# Managed image generation boundary

SciAide currently has no conversation Tool for hosted image generation, no managed wallet, and no automatic OpenRouter credential route. Do not call a nonexistent image Tool, pass application secrets to Shell/Python, or claim an image was generated.

The bundled helper remains source material only. A future user-owned image MCP/API integration may expose a real Tool; until then, report this capability as unavailable. Deterministic plots and diagrams produced by approved Python execution are separate capabilities and must not be presented as generative-image output.
