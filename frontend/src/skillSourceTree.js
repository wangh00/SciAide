/**
 * Builds the tree glyph prefix for a pre-ordered package source entry.
 * Keeping this independent from React makes large/deep package rendering
 * deterministic and cheap to verify.
 * @param {{path:string,kind:string}[]} entries
 * @param {number} index
 */
export function skillTreePrefix(entries, index) {
  const current = entries[index];
  if (!current) return "";
  const parts = current.path.split("/");
  const lines = [];
  for (let depth = 0; depth < parts.length - 1; depth++) {
    const parent = parts.slice(0, depth).join("/");
    const hasLaterSibling = entries.slice(index + 1).some((candidate) => {
      const candidateParts = candidate.path.split("/");
      return candidateParts.length > depth
        && candidateParts.slice(0, depth).join("/") === parent
        && candidateParts[depth] !== parts[depth];
    });
    lines.push(hasLaterSibling ? "│   " : "    ");
  }
  const parent = parts.slice(0, -1).join("/");
  const hasLater = entries.slice(index + 1).some((candidate) => {
    const candidateParts = candidate.path.split("/");
    return candidateParts.slice(0, -1).join("/") === parent;
  });
  return lines.join("") + (hasLater ? "├── " : "└── ");
}

export function createLatestRequestGate() {
  let generation = 0;
  return {
    begin() {
      generation += 1;
      return generation;
    },
    isCurrent(token) {
      return token === generation;
    },
    invalidate() {
      generation += 1;
    },
  };
}
