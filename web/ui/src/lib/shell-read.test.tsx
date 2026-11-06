import { describe, expect, it } from "vitest";
import { codeTokens } from "./code-tokens";
import { type Reading, cutOf, hiddenChars, oneLine, opText, partText, pathsNamed, readCommand } from "./shell-read";
import { CONNECTOR, redirWords } from "./what-ran-words";

// A here-document as an agent writes one: a python script fed on stdin,
// then a grep piped into head.
const HEREDOC = [
  "python - <<'EOF'",
  "p='src/game/scenes/area-sim.ts'",
  "s=open(p,encoding='utf8').read()",
  "old='''  levels?: readonly [number, number];'''",
  "assert old in s",
  "open(p,'w',encoding='utf8').write(s.replace(old, old + '\\n  rareAffixes?: number;'))",
  "EOF",
  'grep -n "opts.rareAffixes\\|constructor(" src/game/scenes/area-sim.ts | head -4',
].join("\n");

const shape = (r: Reading) => r.steps.map((s) => [s.program, s.connector]);

// squash drops the whitespace and line continuations a reading lays out
// differently, so what is left must match the command character for
// character.
const squash = (s: string) => s.replace(/\\\n/g, "").replace(/\s+/g, "");
function rebuilt(r: Reading): string {
  return r.steps.map((s) => s.parts.map(partText).join(" ") + s.heredocs.map((h) => "\n" + h.body + (h.closed ? "\n" + h.delim : "")).join("") + (s.connector && s.connector !== "\n" ? " " + opText(s.connector) : "")).join(" ");
}

describe("readCommand", () => {
  it.each([
    { name: "a here-document then a pipe", src: HEREDOC, want: [["python", "\n"], ["grep", "|"], ["head", ""]] },
    { name: "a cd that guards a test run", src: "cd tools/level-editor && npx vitest run src/area-sim.test.ts 2>&1 | tail -30", want: [["cd", "&&"], ["npx", "|"], ["tail", ""]] },
    { name: "a fallback and a background job", src: "FOO=1 make test >/dev/null 2>build.log || echo failed; echo done &", want: [["make", "||"], ["echo", ";"], ["echo", "&$"]] },
    { name: "a single command", src: "git push --force origin main", want: [["git", ""]] },
    { name: "a newline after && as layout", src: "npm ci &&\n  npm test", want: [["npm", "&&"], ["npm", ""]] },
    { name: "a line continuation", src: "docker compose \\\n  up -d web", want: [["docker", ""]] },
    { name: "a substitution and quotes holding operators", src: "echo \"$(date; uptime)\" 'a | b' && ls", want: [["echo", "&&"], ["ls", ""]] },
    { name: "a comment line", src: "# rebuild\nmake", want: [["", "\n"], ["make", ""]] },
  ])("splits $name into its commands", ({ src, want }) => {
    const r = readCommand(src);
    expect(r.error).toBe("");
    expect(shape(r)).toEqual(want);
  });

  it.each([HEREDOC, "cd a && b 2>&1 | c -x 'y z' > out.txt", "FOO=1 BAR=2 make || true; sleep 1 &", "cat <<-EOT > f.yaml\n\tkey: 1\n\tEOT\necho ok", "echo \"$(git rev-parse --short HEAD)\" # stamp"])(
    "keeps every character of %j", (src) => {
      expect(squash(rebuilt(readCommand(src)))).toBe(squash(src));
    });

  it("reads the here-document body, its delimiter and its language", () => {
    const r = readCommand(HEREDOC);
    const h = r.steps[0].heredocs[0];
    expect(h.delim).toBe("EOF");
    expect(h.closed).toBe(true);
    expect(h.body.split("\n")).toHaveLength(5);
    expect(h.body).toContain("assert old in s");
  });

  it("marks the program, its environment, flags and arguments", () => {
    const s = readCommand("FOO=1 make -j4 test").steps[0];
    expect(s.words.map((w) => [w.text, w.role])).toEqual([["FOO=1", "env"], ["make", "program"], ["-j4", "flag"], ["test", "arg"]]);
  });

  it.each([
    ["printf 'Built %s\\n' \"$(git rev-parse --short HEAD)", 'a " quote opens and never closes'],
    ["echo 'unfinished", "a ' quote opens and never closes"],
    ["echo $(date", "a $( opens and never closes"],
  ])("refuses to guess at %j", (src, want) => {
    expect(readCommand(src)).toEqual({ steps: [], error: want });
  });
});

describe("the words of a reading", () => {
  it.each([
    ["2>&1", "errors into output"],
    [">/dev/null", "output dropped"],
    ["2>/dev/null", "errors dropped"],
    ["> out.txt", "output to out.txt"],
    [">> app.log", "output appended to app.log"],
    ["&> all.log", "output and errors to all.log"],
    ["< in.txt", "input from in.txt"],
    [">&2", "output into errors"],
  ])("says %s as %j", (redir, want) => {
    const r = readCommand("cmd " + redir).steps[0].redirs[0];
    expect(redirWords(r)).toBe(want);
  });

  it("has words for every operator a reading answers", () => {
    for (const c of ["\n", ";", "&&", "||", "|", "|&", "&", "&$"]) expect(CONNECTOR[c]).toBeTruthy();
  });
});

describe("oneLine", () => {
  it("lays a here-document out as its opener and counts the lines", () => {
    expect(oneLine(HEREDOC)).toEqual({ text: "python - <<'EOF' ; grep -n \"opts.rareAffixes\\|constructor(\" src/game/scenes/area-sim.ts | head -4", lines: 8 });
  });
  it("keeps the first line of a command that does not read", () => {
    expect(oneLine("echo 'a\nb")).toEqual({ text: "echo 'a", lines: 2 });
  });
});

describe("the notices", () => {
  it("names each hidden character and its line", () => {
    expect(hiddenChars("git commit -m \"Bump the save​\"\nls‮")).toEqual([
      { cp: "U+00A0", name: "no-break space", line: 1 },
      { cp: "U+200B", name: "zero-width space", line: 1 },
      { cp: "U+202E", name: "right-to-left override", line: 2 },
    ]);
    expect(hiddenChars("plain\ttext\n")).toEqual([]);
  });

  it("reads the cut a producer puts on a long command", () => {
    const hash = "bd53865339da0eb15964543359e8be8955d56b4ecffbe4f1945a624d3b29c49b";
    expect(cutOf("cat > f.json <<'EOF'\n{\"a\":... (921600 bytes, sha256 " + hash + ")")).toEqual({ kept: "cat > f.json <<'EOF'\n{\"a\":", bytes: 921600, sha256: hash });
    expect(cutOf("echo done")).toBeNull();
  });

  it("lists the paths a command names, without addresses or /dev/null", () => {
    expect(pathsNamed("cd web/ui && curl -s https://straza.dev/v1/x > out/res.json 2>/dev/null; cat go.mod")).toEqual(["web/ui", "out/res.json", "go.mod"]);
  });
});

describe("codeTokens", () => {
  it.each([
    ["python", HEREDOC],
    ["javascript", "const a = `x ${1}`; // note\n/* block */ if (a) return 'y';"],
    ["sql", "SELECT id FROM t WHERE name = 'x' -- why\n"],
    ["text", "anything at all"],
  ])("keeps every character of a %s body, in order", (lang, src) => {
    expect(codeTokens(src, lang).map((t) => t.s).join("")).toBe(src);
  });

  it("colors a python triple-quoted string as one string", () => {
    const toks = codeTokens("old='''a\nb'''\nassert old", "python");
    expect(toks.find((t) => t.t === "str")?.s).toBe("'''a\nb'''");
    expect(toks.filter((t) => t.t === "kw").map((t) => t.s)).toEqual(["assert"]);
  });
});
