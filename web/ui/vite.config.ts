import path from "node:path";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { bundleModules, qrLicenseHeader } from "./vite.notices";

// The app is served by strazad under /console/ and compiled into the Go
// embed tree, the way the approvals dist is. The dev proxy exists only so
// the page can be driven against a running strazad before the binary is
// restamped; production builds never carry it.
export default defineConfig({
  base: "/console/",
  plugins: [react(), tailwindcss(), qrLicenseHeader(), bundleModules("pages")],
  resolve: { alias: { "@": path.resolve(__dirname, "./src") } },
  build: {
    outDir: path.resolve(__dirname, "../../internal/server/console/dist"),
    emptyOutDir: true,
    sourcemap: false,
    modulePreload: { polyfill: false },
    // Two entry pages share the kit: the console and the self-service page,
    // each with its own gzipped budget in distbudget.Budgets.
    // Stable names, no content hash: the dist is committed and strazad
    // serves it with Cache-Control no-cache, so a hash bought nothing and
    // cost a rename of every chunk in each console commit's history.
    rollupOptions: {
      input: { console: path.resolve(__dirname, "index.html"), self: path.resolve(__dirname, "self-service.html") },
      output: { entryFileNames: "assets/[name].js", chunkFileNames: "assets/[name].js", assetFileNames: "assets/[name][extname]" },
    },
  },
  // The suites run over the source under jsdom (make ui-test); the built
  // dist is checked by the Go test in internal/server/console.
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.tsx"],
    css: false,
  },
  server: {
    host: "127.0.0.1",
    port: 5179,
    strictPort: true,
    proxy: { "/v1": { target: process.env.STRAZA_API || "http://127.0.0.1:8420", changeOrigin: false } },
  },
});
