import path from "node:path";
import { defineConfig } from "vite";
import { bundleModules } from "./vite.notices";

// The self-service service worker is built on its own as one classic
// script at the dist root, dist/sw.js, after the app build. A worker's
// scope is the directory it is served from, strazad serves this file at
// /self-service/sw.js, and a classic script imports nothing at runtime, so
// the record, the push wire shapes and the call summary it shares with the
// page are inlined here from the same source.
export default defineConfig({
  plugins: [bundleModules("sw")],
  build: {
    outDir: path.resolve(__dirname, "../../internal/server/console/dist"),
    emptyOutDir: false,
    sourcemap: false,
    lib: {
      entry: path.resolve(__dirname, "src/self-service/sw.ts"),
      formats: ["iife"],
      name: "strazaSelfServiceWorker",
      fileName: () => "sw.js",
    },
  },
});
