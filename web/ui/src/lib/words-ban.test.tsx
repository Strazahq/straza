import { describe, expect, it } from "vitest";

// Every source file of both entry pages, as text. Tests and fixtures are
// left out, because they may quote an old text to prove it is gone.
const sources = import.meta.glob<string>(["../**/*.{ts,tsx}", "!../**/*.test.{ts,tsx}", "!../test/**"], {
  query: "?raw",
  import: "default",
  eager: true,
});

// BANNED are the filler phrases the console does not use. Each
// says nothing a person acts on, or names Straza by a word that means the
// MCP server on the same screen.
const BANNED: [RegExp, string][] = [
  [/or nothing changes/i, "a publish needs no tail about what Cancel does"],
  [/in one step/i, "the count of steps is not a fact a person acts on"],
  [/nothing changes until/i, "an admin knows what a draft is"],
  [/nothing is live until/i, "an admin knows what a draft is"],
  [/you can [a-z ]+ later/i, "say where the control is, or say nothing"],
  [/checked just now/i, "a time that is always now is filler"],
  [/a moment ago/i, "a time that is always now is filler"],
  [/the server refused it/i, "the daemon is Straza, the server is the MCP server"],
  [/HTTP, streamable/, "the transport is called Streamable HTTP"],
  [/\breads (running|stopped|degraded|paused)\b/, "a state is, it does not read"],
];

describe("the words of the console", () => {
  it("hold none of the banned filler phrases", () => {
    const hits: string[] = [];
    for (const [file, text] of Object.entries(sources)) {
      text.split("\n").forEach((line, i) => {
        if (/^\s*(\/\/|\*|\/\*)/.test(line)) return;
        for (const [re, why] of BANNED) {
          if (re.test(line)) hits.push(file + ":" + (i + 1) + " " + re + " (" + why + ")");
        }
      });
    }
    expect(hits).toEqual([]);
  });
});
