import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react({ jsxRuntime: "automatic" }), tailwindcss()],
  cacheDir: ".cache/vite",
  resolve: {
    alias: {
      "@/registry/primitives/animate/slot": fileURLToPath(new URL("./src/components/animate/slot.tsx", import.meta.url)),
      "@workspace/ui/lib/utils": fileURLToPath(new URL("./src/lib/utils.ts", import.meta.url)),
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    host: "127.0.0.1",
    strictPort: true,
    fs: { strict: true },
    proxy: { "/api": { target: "http://127.0.0.1:8080", changeOrigin: false } },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
