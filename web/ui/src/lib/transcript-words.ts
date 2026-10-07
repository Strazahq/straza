// The sentences of the Transcripts screen and the conversation sheet.
import type { CaptureConfig, Transcript } from "./api";
import { absTime, dayOf } from "./words";

// captureLine is the posture line under the title, read live from the
// config. Unreadable renders as unknown, never as off.
export function captureLine(c: CaptureConfig | null | undefined, failed: boolean): { tone: "ok" | "plain" | "unknown"; word: string; text: string } {
  if (failed) return { tone: "unknown", word: "unknown", text: "Recording settings are unavailable." };
  if (!c) return { tone: "unknown", word: "reading", text: "" };
  if (c.policy_sets === undefined) return { tone: "unknown", word: "unknown", text: "Recording settings are incomplete." };
  if (c.policy_sets === 0) return { tone: "plain", word: "off", text: "No active policy currently records sessions." };
  const hours = c.retention_hours;
  const retention = hours === undefined ? "Retention is unavailable" : "Recorded messages are deleted after " + (hours >= 48 ? Math.round(hours / 24) + " d" : hours + " h");
  const bodies = c.body_store === "s3" ? "message bodies are stored in S3 and follow its lifecycle rules" : c.body_store === "inline" ? "message bodies are stored in the database" : "message storage is unavailable";
  const mode = c.mode === "redact" ? "secrets masked" : c.mode === "verbatim" ? "word for word" : c.mode === "mixed" ? "word for word or with secrets masked, depending on the policy" : "recording mode unavailable";
  return { tone: "ok", word: "on", text: c.policy_sets + (c.policy_sets === 1 ? " live policy records" : " live policies record") + " sessions, " + mode + ". " + retention + "; " + bodies + "." };
}

export const modeWord = (mode: string | undefined) => (mode === "verbatim" ? "recorded word for word" : "recorded with secrets masked");
export const kindWord = (t: { kind: string; agent_type?: string }) => (t.kind === "prompt" ? "prompt" : t.agent_type ? "reply · " + t.agent_type : "reply");
export const speaker = (t: { kind: string; agent_type?: string }, username: string) => (t.kind === "prompt" ? username || "user" : t.agent_type ? "assistant · " + t.agent_type : "assistant");

export const MATCH_CONTAINS = "contains text";
export const MATCH_EXACT = "exact value";
export const EXACT_HINT = "Exact value: the text is hashed in this browser and only the hash is sent, so a hunted secret never leaves this machine.";
export const CONTAINS_HINT = "Contains text: the server scans recorded prompts and replies for the words.";
export const EXACT_NEEDS_SECURE = "Hashing in the browser needs a secure context (TLS or localhost), so exact value is not available here.";
export const TYPE_WHAT = "Type what to search for.";
export const INBOX_LINE = "One row per recorded session, newest activity first. Click a row for the whole conversation. Recording is turned on by policy, so an empty list is normal.";
export const hitsWords = (n: number) => (n === 1 ? "1 turn matches." : n + " turns match.");
export const loadedWords = (n: number) => n + (n === 1 ? " conversation" : " conversations") + " loaded";
export const GOTO_LABEL = "Go to session";
export const EMPTY_INBOX = "Nothing has been recorded yet. Recording is turned on by policy: a policy with recording on records the sessions it matches.";
export const EMPTY_HITS = "No recorded messages match this search.";
export const EMPTY_TURNS = "No recorded turns: recording is off for this session's policy, or nothing was said.";
export const BODY_UNAVAILABLE = "(body unavailable)";
export const BODY_MISSING = "The body of this turn should be in the body store and was not found. This is an integrity finding; the hash witness on the audit chain still stands.";
export const truncatedLine = (hash: string) => "Truncated. Full content hash " + hash;

// What the screen and the sheet say while they are reading, and what the
// sheet says in place of the turn count when the read failed.
export const READING_LIST = "Reading the recorded conversations.";
export const SEARCHING = "Searching the recorded turns.";
export const READING_TURNS = "Reading the recorded turns of this session.";
export const TURNS_UNREAD = "The recorded turns could not be read.";

// clockTime is the wall clock part of a stamp in the active zone. A turn
// carries the clock and the day band above it carries the day.
export const clockTime = (iso: string) => absTime(iso).slice(11, 19);

// turnsLine is the sheet's description: whose session it is, how many turns
// were captured, and when the conversation ran. A conversation inside one
// day names the day once and the clocks; one across midnight names both
// stamps in full.
export function turnsLine(username: string | undefined, n: number, from: string, to: string): string {
  const who = (username || "The user is not named") + ". " + n + (n === 1 ? " turn" : " turns");
  const span = dayOf(from) === dayOf(to)
    ? " on " + dayOf(from) + " from " + clockTime(from) + " to " + clockTime(to)
    : " from " + absTime(from).slice(0, 19) + " to " + absTime(to).slice(0, 19);
  return who + span + ".";
}

// conversationText is the conversation as a reviewer reads it off the
// console: a header naming the session, the user and the recording mode,
// then one block per turn with the zone-labelled stamp and the speaker. A
// lost body and a truncated turn say so in the sheet's words.
export function conversationText(t: Transcript): string {
  const turns = t.turns || [];
  const first = turns[0];
  const last = turns[turns.length - 1];
  const span = first
    ? modeWord(first.mode).replace(/^r/, "R") + ". " + turns.length + (turns.length === 1 ? " turn" : " turns") + " from " + absTime(first.at) + " to " + absTime(last.at) + "."
    : EMPTY_TURNS;
  const head = ["Session " + t.session_id, "User: " + (t.username || "not named"), span];
  const blocks = turns.map((turn) => {
    const body = turn.body_missing ? BODY_UNAVAILABLE + " " + BODY_MISSING + " Hash " + turn.content_hash : turn.content;
    const tail = turn.truncated ? "\n" + truncatedLine(turn.content_hash) : "";
    return "[" + absTime(turn.at) + "] " + speaker(turn, t.username || "") + ":\n" + body + tail;
  });
  return head.join("\n") + "\n\n" + blocks.join("\n\n") + "\n";
}

// conversationJSONL is one JSON object per turn: the turn as the API
// answered it plus the session id and the username, so a line stands on its
// own once it sits in a SIEM.
export const conversationJSONL = (t: Transcript) => (t.turns || []).map((turn) => JSON.stringify({ session_id: t.session_id, username: t.username, ...turn })).join("\n") + "\n";
