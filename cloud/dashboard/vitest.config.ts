import { defineConfig, mergeConfig } from "vitest/config";
import path from "node:path";
import viteConfig from "./vite.config.ts";

export default mergeConfig(
  viteConfig,
  defineConfig({
    resolve: {
      alias: {
        "@tests": path.resolve(import.meta.dirname, "./tests"),
      },
    },
    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: ["./tests/setup.ts"],
      include: ["tests/**/*.{test,spec}.{ts,tsx}"],
      restoreMocks: true,
      coverage: {
        provider: "v8",
        reporter: ["text-summary", "text"],
        include: ["src/**/*.{ts,tsx}"],
        exclude: ["src/main.tsx", "src/vite-env.d.ts", "src/types/**"],
      },
    },
  }),
);
