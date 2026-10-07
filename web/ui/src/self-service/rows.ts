// The approver lane's rows read through the console's record shape. The
// Requests tab, the request dialog and the decide dialog are the Approvals
// area's components, which read an ApprovalRow; the approver API answers a
// different shape (approverRow in internal/server/approver_queue.go), so
// this is the one mapping between the two. A row the person may decide
// carries a challenge, which is the server's only signal of that.
import type { ApprovalRow } from "@/lib/api";
import { type SummaryView, machineText } from "./summary";

// ApproverRow is one row of GET /v1/approver/pending and history, as the
// wire has it.
export type ApproverRow = {
  id: string;
  created_at: string;
  expires_at: string;
  requester: { username?: string; kind?: string };
  origin: { actor?: string };
  session_id?: string;
  harness?: string;
  rule_id: string;
  set_name: string;
  summary: SummaryView;
  justification?: string;
  challenge?: string;
  state?: string;
  decided_by?: string;
  decided_at?: string | null;
  decided_via?: { surface?: string; device_id?: string } | null;
  decided_reason?: string;
  mode?: string;
  approver_roles?: string[];
  approver_users?: string[];
  class?: string;
  grant_expires_at?: string | null;
  consumed_at?: string | null;
  consumed_by?: string;
  args_preview?: string;
  args_truncated?: boolean;
  args_bytes?: number;
  argv_hash_prefix?: string;
  binding_scope?: string;
};

// SelfRow is the console's record plus the challenge the approver lane
// puts on a row the person may decide.
export type SelfRow = ApprovalRow & { challenge?: string; requesterKind?: string };

// summaryString rebuilds the stored summary the console parses: the wire
// identity for an MCP call, and the command after the kind for a hook
// call, which the redacted preview carries.
export function summaryString(r: Pick<ApproverRow, "summary" | "args_preview">): string {
  const s = r.summary || {};
  if (s.tool === "mcp.call") return machineText(s);
  const line = (r.args_preview || "").split("\n")[0].trim();
  return line && s.tool ? s.tool + ": " + line : machineText(s);
}

// toSelfRow maps one approver row onto the console's shape. The lane is
// read from the call: an MCP call came through the gateway, anything else
// from the hook.
export function toSelfRow(r: ApproverRow): SelfRow {
  const username = (r.requester && r.requester.username) || "";
  const via = r.decided_via || null;
  return {
    id: r.id,
    state: r.state || "pending",
    createdAt: r.created_at,
    expiresAt: r.expires_at,
    decidedAt: r.decided_at ?? null,
    user: username,
    username,
    session: r.session_id || "",
    rule: r.rule_id,
    set: r.set_name,
    lane: r.summary && r.summary.tool === "mcp.call" ? "gateway" : "hook",
    summary: summaryString(r),
    justification: r.justification || "",
    approverRoles: r.approver_roles || [],
    approverUsers: r.approver_users || [],
    selfApproval: false,
    mode: r.mode || "",
    decidedBy: r.decided_by || "",
    decidedByName: r.decided_by || "",
    channel: via && via.surface ? via.surface : undefined,
    decidedReason: r.decided_reason,
    decidedDeviceId: via && via.device_id ? via.device_id : undefined,
    class: r.class,
    grantExpiresAt: r.grant_expires_at ?? null,
    consumedAt: r.consumed_at ?? null,
    consumedBy: r.consumed_by,
    argsPreview: r.args_preview,
    argsTruncated: r.args_truncated,
    argsBytes: r.args_bytes,
    argvHashPrefix: r.argv_hash_prefix,
    bindingScope: r.binding_scope,
    challenge: r.challenge,
    requesterKind: r.requester && r.requester.kind,
  };
}

// decidable says whether the server handed this browser the nonce to sign:
// the only reading of "yours to decide" the page makes.
export const decidable = (r: Pick<SelfRow, "challenge" | "state">): boolean => r.state === "pending" && !!r.challenge;
