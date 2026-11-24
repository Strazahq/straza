import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// Allowed is a class the guard lets through, with its reason. It holds where
// the class stands in one class list with the class `beside`, or on a line
// of `file` that holds `line`.
type Allowed = { cls: string; beside?: string; file?: string; line?: string; reason: string };

const ALLOWED: Allowed[] = [
  { cls: "text-base", beside: "md:text-dense", reason: "A text field keeps 16 px below md, because iOS zooms into a focused field set smaller." },
  { cls: "rounded-[2px]", file: "components/ui/tooltip.tsx", line: "TooltipPrimitive.Arrow", reason: "The tooltip arrow is a 10 px square turned 45 degrees, and 6 px would round it into a circle." },
];

const SRC = dirname(fileURLToPath(import.meta.url));
const SIZE = /(?<![\w-])text-(?:xs|sm|base|lg|[2-9]?xl|\[\d+(?:\.\d+)?(?:px|rem)\])(?![\w-])/g;
const RADIUS = /(?<![\w-])rounded(?:-[^\s"'`;,)}]+)?(?!\w)/g;
const RADIUS_OK = /^rounded-(?:md|pill|full|none|(?:t|r|b|l|s|e|tl|tr|br|bl|ss|se|ee|es)-md)$/;
const FONT_SIZE = /font-size\s*:\s*[-+]?\.?\d/;

function sources(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) sources(p, out);
    else if (/\.(ts|tsx|css)$/.test(e.name) && !/\.test\.tsx?$/.test(e.name)) out.push(p);
  }
  return out;
}

const FILES = sources(SRC).map((p) => ({ rel: relative(SRC, p).split("\\").join("/"), lines: readFileSync(p, "utf8").split("\n") }));

// A line that is only a comment paints nothing, so the guard reads past it.
const comment = (line: string) => /^\s*(\/\/|\/\*|\*|\{\/\*)/.test(line);

// The class list around a match: the run between the nearest quotes on the line.
function classList(line: string, at: number): string[] {
  let a = at;
  while (a > 0 && !/["'`]/.test(line[a - 1])) a--;
  let b = at;
  while (b < line.length && !/["'`]/.test(line[b])) b++;
  return line.slice(a, b).split(/\s+/);
}

function allowed(rel: string, line: string, at: number, cls: string): boolean {
  return ALLOWED.some((x) => x.cls === cls
    && (x.file === undefined || x.file === rel)
    && (x.line === undefined || line.includes(x.line))
    && (x.beside === undefined || (line[at - 1] !== ":" && classList(line, at).includes(x.beside))));
}

function offenders(re: RegExp, bad: (cls: string) => boolean): string[] {
  const out: string[] = [];
  for (const f of FILES) {
    f.lines.forEach((line, i) => {
      if (comment(line)) return;
      for (const m of line.matchAll(re)) {
        if (bad(m[0]) && !allowed(f.rel, line, m.index, m[0])) out.push(`${f.rel}:${i + 1}: ${m[0]}`);
      }
    });
  }
  return out;
}

describe("the console's text sizes and radii come from tokens.css", () => {
  it("reads the console's source files", () => {
    expect(FILES.length).toBeGreaterThan(100);
  });

  it("uses no Tailwind text size outside caption, dense, body, section, title and stat", () => {
    expect(offenders(SIZE, () => true)).toEqual([]);
  });

  it("sets no CSS font size as a number outside tokens.css", () => {
    const out: string[] = [];
    for (const f of FILES) {
      if (!f.rel.endsWith(".css") || f.rel === "tokens.css") continue;
      f.lines.forEach((line, i) => { if (FONT_SIZE.test(line)) out.push(`${f.rel}:${i + 1}: ${line.trim()}`); });
    }
    expect(out).toEqual([]);
  });

  it("uses no radius class outside rounded-md, rounded-pill, rounded-full, rounded-none and the sided md forms", () => {
    expect(offenders(RADIUS, (cls) => !RADIUS_OK.test(cls))).toEqual([]);
  });
});
