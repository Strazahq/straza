import { describe, expect, it } from "vitest";
import { lineDiff } from "./line-diff";

const shape = (before: string, after: string) => lineDiff(before, after).map((l) => l.kind[0] + l.text);

describe("the YAML tab's line diff", () => {
  it("leaves an unchanged text plain", () => {
    expect(shape("a\nb\nc", "a\nb\nc")).toEqual(["sa", "sb", "sc"]);
  });

  it("marks a line the edit replaced, removed first", () => {
    expect(shape("a\nb\nc", "a\nB\nc")).toEqual(["sa", "rb", "aB", "sc"]);
  });

  it("marks the lines an edit added inside the text", () => {
    expect(shape("a\nc", "a\nb1\nb2\nc")).toEqual(["sa", "ab1", "ab2", "sc"]);
  });

  it("marks the lines an edit removed", () => {
    expect(shape("a\nb\nc", "a\nc")).toEqual(["sa", "rb", "sc"]);
  });

  it("reads an empty side as a whole add or a whole remove", () => {
    expect(shape("", "a\nb")).toEqual(["aa", "ab"]);
    expect(shape("a\nb", "")).toEqual(["ra", "rb"]);
  });

  it("keeps the shared head and tail of a long text plain", () => {
    const before = Array.from({ length: 400 }, (_, i) => "line " + i).join("\n");
    const after = before.replace("line 200", "line two hundred");
    const out = lineDiff(before, after);
    expect(out.filter((l) => l.kind !== "same").map((l) => l.kind + " " + l.text)).toEqual(["remove line 200", "add line two hundred"]);
    expect(out.length).toBe(401);
  });

  it("keeps every line of both texts, in order", () => {
    const out = lineDiff("one\ntwo\nthree", "one\nthree\nfour");
    expect(out.filter((l) => l.kind !== "add").map((l) => l.text)).toEqual(["one", "two", "three"]);
    expect(out.filter((l) => l.kind !== "remove").map((l) => l.text)).toEqual(["one", "three", "four"]);
  });
});
