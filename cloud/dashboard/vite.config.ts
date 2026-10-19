import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

const API_TARGET = process.env.VITE_API_PROXY_TARGET ?? "http://localhost:8910";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    rollupOptions: {
      output: {
        manualChunks(id): string | undefined {
          if (!id.includes("node_modules")) return undefined;
          if (/[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/.test(id)) return "react";
          if (id.includes("react-router")) return "router";
          if (id.includes("@tanstack")) return "query";
          return undefined;
        },
      },
    },
    chunkSizeWarningLimit: 260,
  },
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  server: {
    port: 3910,
    strictPort: true,
    proxy: {
      "/api": { target: API_TARGET, changeOrigin: false },
      "/auth": { target: API_TARGET, changeOrigin: false },
      "/billing": { target: API_TARGET, changeOrigin: false },
      "/docs/openapi.yaml": { target: API_TARGET, changeOrigin: false },
      "/docs/config.json": { target: API_TARGET, changeOrigin: false },
    },
  },
});
