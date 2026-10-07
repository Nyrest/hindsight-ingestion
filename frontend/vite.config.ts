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
