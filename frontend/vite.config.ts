import path from "node:path";
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const root = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    {
      // The post-processing editor only edits JavaScript: drop Monaco's other
      // languages and their workers from the embedded bundle.
      name: "monaco-javascript-only",
      enforce: "pre",
      resolveId(source, importer) {
        if (!/\/monaco-editor\/esm\/vs\/(index|editor\/editor\.main)\.js$/.test(importer?.replaceAll("\\", "/") ?? "")) return null;
        if (/languages\/features\/(css|html|json)\/register\.js$/.test(source)) return "\0monaco-stub";
        if (/languages\/definitions\/(?!javascript\/|typescript\/)[^/]+\/register\.js$/.test(source)) return "\0monaco-stub";
        return null;
      },
      load(id) {
        return id === "\0monaco-stub" ? "export {};" : null;
      },
    },
    {
      name: "keep-embed-placeholder",
      closeBundle() {
        // Keep backend-only builds working in a fresh source checkout.
        writeFileSync(path.resolve(root, "../backend/internal/web/dist/.gitkeep"), "");
      },
    },
  ],
  resolve: {
    alias: { "@": path.resolve(root, "src") },
  },
  build: {
    outDir: "../backend/internal/web/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": { target: "http://localhost:8080", changeOrigin: true },
    },
  },
});
