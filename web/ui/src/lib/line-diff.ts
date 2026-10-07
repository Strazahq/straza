// The line diff the YAML tab shows against the live version. It compares
// whole lines, since a policy is read line by
// line and a character diff would say less about a moved rule than a
// line diff does.

export type DiffLine = { kind: "same" | "add" | "remove"; text: string };

// CELLS caps the dynamic table a long pair of texts would build. Past the
// cap the diff falls back to the whole of one side removed and the whole of
// the other added, which is still true, only coarser.
const CELLS = 4_000_000;

const split = (text: string): string[] => (text === "" ? [] : text.split("\n"));

// middle runs the longest common subsequence over the lines that differ,
// after the shared head and tail are set aside.
function middle(before: string[], after: string[]): DiffLine[] {
  const n = before.length;
  const m = after.length;
  if (n === 0) return after.map((text) => ({ kind: "add" as const, text }));
  if (m === 0) return before.map((text) => ({ kind: "remove" as const, text }));
  if (n * m > CELLS) {
    return [
      ...before.map((text) => ({ kind: "remove" as const, text })),
      ...after.map((text) => ({ kind: "add" as const, text })),
    ];
  }
  // len[i][j] is the length of the longest common subsequence of before
  // from i and after from j, filled backwards so the walk below reads it
  // forwards.
  const len = new Uint32Array((n + 1) * (m + 1));
  const at = (i: number, j: number) => i * (m + 1) + j;
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      len[at(i, j)] = before[i] === after[j] ? len[at(i + 1, j + 1)] + 1 : Math.max(len[at(i + 1, j)], len[at(i, j + 1)]);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (before[i] === after[j]) {
      out.push({ kind: "same", text: before[i] });
      i++;
      j++;
    } else if (len[at(i + 1, j)] >= len[at(i, j + 1)]) {
      out.push({ kind: "remove", text: before[i] });
      i++;
    } else {
      out.push({ kind: "add", text: after[j] });
      j++;
    }
  }
  while (i < n) out.push({ kind: "remove", text: before[i++] });
  while (j < m) out.push({ kind: "add", text: after[j++] });
  return out;
}

// lineDiff reads the stored text and the working text into the lines the
// editor paints: removed lines in the danger tone, added lines in the ok
// tone, and the lines both texts share left plain. The shared head and
// tail are trimmed first, so an edit inside a long document compares a few
// lines rather than the whole of it.
export function lineDiff(before: string, after: string): DiffLine[] {
  const a = split(before);
  const b = split(after);
  let head = 0;
  while (head < a.length && head < b.length && a[head] === b[head]) head++;
  let tail = 0;
  while (tail < a.length - head && tail < b.length - head && a[a.length - 1 - tail] === b[b.length - 1 - tail]) tail++;
  return [
    ...a.slice(0, head).map((text) => ({ kind: "same" as const, text })),
    ...middle(a.slice(head, a.length - tail), b.slice(head, b.length - tail)),
    ...a.slice(a.length - tail).map((text) => ({ kind: "same" as const, text })),
  ];
}
