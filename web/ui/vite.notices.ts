import fs from "node:fs";
import path from "node:path";
import { normalizePath, type Plugin } from "vite";

// The minifier drops every comment, and the QR encoder's MIT license asks
// that its notice travel with each copy. This puts the vendored file's
// license header back on top of the one lazy chunk that carries the encoder,
// so no entry page pays for it. The notices of every other package are in
// dist/THIRD_PARTY_NOTICES.txt, which tools/uibuild writes after this build.
export function qrLicenseHeader(): Plugin {
  const source = path.resolve(__dirname, "src/vendor/qrcodegen.js");
  return {
    name: "straza-qr-license-header",
    apply: "build",
    generateBundle(_, bundle) {
      const header = /^\/\*[\s\S]*?\*\//.exec(fs.readFileSync(source, "utf8"))?.[0];
      if (!header) this.error(`${source} has no license header: re-vendor it from upstream with its header`);
      for (const out of Object.values(bundle)) {
        if (out.type === "chunk" && out.moduleIds.includes(normalizePath(source))) out.code = `${header}\n${out.code}`;
      }
    },
  };
}

// The id of every module in the chunks of one build, relative to web/ui and
// without the bundler's virtual prefix and query, goes to
// node_modules/.straza/bundle-<name>.json. tools/notices reads the lists, so
// a package whose code is bundled gets its notice whatever the lock file
// says of it, and a module of unknown origin stops the build of the notices.
export function bundleModules(name: string): Plugin {
  return {
    name: "straza-bundle-modules",
    apply: "build",
    generateBundle(_, bundle) {
      const ids = new Set<string>();
      for (const out of Object.values(bundle)) {
        if (out.type !== "chunk") continue;
        for (const id of out.moduleIds) {
          const file = id.replace(/^\0/, "").split("?")[0];
          ids.add(path.isAbsolute(file) ? normalizePath(path.relative(__dirname, file)) : file);
        }
      }
      const dir = path.resolve(__dirname, "node_modules/.straza");
      fs.mkdirSync(dir, { recursive: true });
      fs.writeFileSync(path.join(dir, `bundle-${name}.json`), JSON.stringify([...ids].sort(), null, 1) + "\n");
    },
  };
}
