// The word-level diff of an edited rule's sentence, which the review page
// shows as the old sentence struck through above the new one with the
// words that differ marked. No React and no sentence here.

// Part is a run of words of one side, and changed says the other side does
// not hold it.
export type Part = { text: string; changed: boolean };

// wordDiff compares two sentences word by word, a word being a run of text
// between spaces, so punctuation stays on its word. It answers each side
// as runs of words, the words outside their longest common subsequence
// marked changed, and the runs joined by single spaces.
export function wordDiff(was: string, now: string): { was: Part[]; now: Part[] } {
  const a = was.split(/\s+/).filter(Boolean);
  const b = now.split(/\s+/).filter(Boolean);
  // keep[i][j] is the length of the longest common subsequence of a[i:]
  // and b[j:].
  const keep = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      keep[i][j] = a[i] === b[j] ? keep[i + 1][j + 1] + 1 : Math.max(keep[i + 1][j], keep[i][j + 1]);
    }
  }
  const left: Part[] = [];
  const right: Part[] = [];
  const push = (side: Part[], text: string, changed: boolean) => {
    const last = side[side.length - 1];
    if (last && last.changed === changed) last.text += " " + text;
    else side.push({ text, changed });
  };
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      push(left, a[i++], false);
      push(right, b[j++], false);
    } else if (keep[i + 1][j] >= keep[i][j + 1]) {
      push(left, a[i++], true);
    } else {
      push(right, b[j++], true);
    }
  }
  while (i < a.length) push(left, a[i++], true);
  while (j < b.length) push(right, b[j++], true);
  return { was: left, now: right };
}
