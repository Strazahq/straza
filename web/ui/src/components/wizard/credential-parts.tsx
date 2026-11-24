import type * as React from "react";
import { BanIcon } from "lucide-react";
import { Pill } from "@/components/chip";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { ProviderRow } from "@/lib/api";
import { SENT_AS } from "@/lib/manifest";
import {
  AGENT_ROWS, BASIC_NOTE, CALLER_NOT_HERE, CREDENTIAL_CHOICES, ENV_WAY, NOT_AVAILABLE, NO_PROVIDER_YET, PROVIDERS_UNREAD, credentialSay, sendsWhat,
} from "@/lib/words";
import { ERROR, LAB, RadioRow, RowGroup } from "./parts";

// The credential choices as the Add MCP server wizard and the Change
// credential sheet of a server's page both draw them: one row per choice,
// the fields of the picked one under it.

// credentialOff says why a credential choice is not available, or "" when
// it is: a caller's own credential needs a server over HTTP, and a sign-in
// needs an identity provider from a list that was read. providers is
// undefined while the list is still being read, which leaves a sign-in open.
export function credentialOff(kind: string, http: boolean, providers: ProviderRow[] | null | undefined): string {
  if (kind !== "token" && kind !== "oauth") return "";
  if (!http) return CALLER_NOT_HERE;
  if (kind === "oauth" && Array.isArray(providers) && providers.length === 0) return NO_PROVIDER_YET;
  if (kind === "oauth" && providers === null) return PROVIDERS_UNREAD;
  return "";
}

const OFF_PILL = "border border-dashed border-input bg-transparent px-[9px] py-px text-muted-foreground";

type RowsProps = { value: string; onPick: (kind: string) => void; off: (kind: string) => string; provider: string; children: React.ReactNode };

// CredentialRows is the four choices: the label, the fact as a pill, the
// reason a choice is not available, and under the picked one its short
// sentence and the fields children holds.
export function CredentialRows({ value, onPick, off, provider, children }: RowsProps) {
  return (
    <RowGroup label="credential" value={value} onPick={onPick}>
      {CREDENTIAL_CHOICES.map((c) => {
        const why = off(c.kind);
        const on = value === c.kind;
        return (
          <RadioRow key={c.kind} value={c.kind} label={c.label} on={on} off={!!why} line={why}
            fact={why ? <Pill icon={BanIcon} className={OFF_PILL}>{NOT_AVAILABLE}</Pill> : <Pill>{c.fact}</Pill>}>
            {on && <><p className="m-0 text-dense text-text-2">{credentialSay(c.kind, provider)}</p>{children}</>}
          </RadioRow>
        );
      })}
    </RowGroup>
  );
}

type SentAsProps = { keys: string[]; value: string; onPick: (v: string) => void; optionLabel?: (key: string) => string };

// SentAs is how the secret travels: a picker of the ways keys offers, or
// the one way of a process runtime as a fact. optionLabel names an option
// the SENT_AS table does not have.
export function SentAs({ keys, value, onPick, optionLabel }: SentAsProps) {
  return (
    <div className="flex flex-col gap-1">
      <span className={LAB}>Sent as</span>
      {keys.length === 1 && keys[0] === "env" ? (
        <div className="flex min-h-9 items-center"><Pill>{ENV_WAY}</Pill></div>
      ) : (
        <Select value={value} onValueChange={onPick}>
          <SelectTrigger aria-label="sent as" className="w-full"><SelectValue /></SelectTrigger>
          <SelectContent>{keys.map((k) => <SelectItem key={k} value={k}>{optionLabel ? optionLabel(k) : SENT_AS[k].label}</SelectItem>)}</SelectContent>
        </Select>
      )}
    </div>
  );
}

type NameProps = { id: string; env: boolean; error?: string; input: React.ComponentProps<"input"> };

// SentName is the name a named header or an environment variable needs,
// with the error under it when it is missing. input carries the value and
// its handlers.
export function SentName({ id, env, error, input }: NameProps) {
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className={LAB}>{env ? "Variable name" : "Header name"}</label>
      <Input id={id} aria-label={env ? "variable name" : "header name"} placeholder={env ? "API_TOKEN" : "X-Api-Key"} spellCheck={false} autoComplete="off" className="font-mono"
        aria-invalid={!!error || undefined} aria-describedby={id + "-say"} {...input} />
      {error && <p id={id + "-say"} className={ERROR}>{error}</p>}
    </div>
  );
}

type SendsProps = { sent: string; cred: string; headerName: string; envName: string };

// Sends is the live preview of what travels with each call, in mono, with
// how to type a basic-auth secret.
export function Sends({ sent, cred, headerName, envName }: SendsProps) {
  return (
    <p className="m-0 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-caption text-muted-foreground" data-sends>
      <span>Sends</span>
      <code className="min-w-0 rounded-md border border-border bg-secondary px-1.5 font-mono text-caption leading-5 text-foreground [overflow-wrap:anywhere]">{sendsWhat(sent, cred, headerName, envName)}</code>
      {sent === "basic" && <span>{BASIC_NOTE}</span>}
    </p>
  );
}

type AgentsProps = { kind: "token" | "oauth"; value: string; onPick: (v: string) => void; lineOf?: (row: { value: string; line: string }) => string };

// AgentsPicker is what an agent with no credential of its own runs on, as
// small rows. Only the picked row says its line. lineOf words a row's line
// another way.
export function AgentsPicker({ kind, value, onPick, lineOf }: AgentsProps) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className={LAB}>{"Agents with no " + (kind === "token" ? "token" : "sign-in") + " of their own"}</span>
      <RowGroup label="agents" value={value} onPick={onPick}>
        {AGENT_ROWS[kind].map((r) => (
          <RadioRow key={r.value} small value={r.value} label={r.name} on={value === r.value} line={value === r.value ? (lineOf ? lineOf(r) : r.line) : ""} />
        ))}
      </RowGroup>
    </div>
  );
}
