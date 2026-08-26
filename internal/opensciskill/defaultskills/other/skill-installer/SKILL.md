---
name: skill-installer
description: Explain how third-party Skills are installed or removed through SciAide's reviewed local management interface.
category: other
entry: false
---

# Third-party Skill management

SciAide does not expose Skill installation as an agent command. Installation and removal are user-owned management operations in the **Skills** page.

- Git installs are pinned to a commit SHA and pass local path, size, link, and content review.
- Warnings require explicit confirmation for the same reviewed SHA.
- Removal uses a recoverable local archive.
- Installation never grants Tool, MCP, filesystem, process, or secret permissions.

Do not run an upstream CLI, write directly into a Skill store, or claim cloud synchronization. Direct the user to the Skills page and report this host boundary honestly.
