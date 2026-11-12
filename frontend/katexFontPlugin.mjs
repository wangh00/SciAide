import postcss from "postcss";

// SciAide runs in WebView2, which supports WOFF2. Keep every KaTeX family and
// weight, but don't ship legacy WOFF/TTF copies of those same fonts.
export function katexFontPlugin() {
  return {
    postcssPlugin: "sciaide-katex-woff2",
    AtRule: {
      "font-face"(rule) {
        const family = rule.nodes?.find(node => node.type === "decl" && node.prop === "font-family");
        if (!family || !/^['"]?KaTeX_/.test(family.value)) return;
        const source = rule.nodes.find(node => node.type === "decl" && node.prop === "src");
        if (!source) return;
        const woff2 = source.value.match(/url\([^)]*\)\s*format\(["']woff2["']\)/);
        if (woff2) source.value = woff2[0];
      },
    },
  };
}

// Run before Vite's CSS URL handling: a normal PostCSS visitor can remove the
// fallback references yet leave their already-emitted assets in the bundle.
export function katexWoff2Plugin() {
  return {
    name: "sciaide-katex-woff2-source",
    enforce: "pre",
    async transform(code, id) {
      if (!/\/katex\/dist\/katex(?:\.min)?\.css(?:\?|$)/.test(id.replaceAll("\\", "/"))) return;
      const result = await postcss([katexFontPlugin()]).process(code, { from: id, map: false });
      return { code: result.css, map: null };
    },
  };
}
