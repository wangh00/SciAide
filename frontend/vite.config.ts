import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { katexWoff2Plugin } from "./katexFontPlugin.mjs";

export default defineConfig({
  plugins: [katexWoff2Plugin(), react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    strictPort: true,
  },
});
