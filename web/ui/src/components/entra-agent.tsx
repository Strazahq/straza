import { ExternalLinkIcon } from "lucide-react";
import { Chip, Pill, Tag } from "@/components/chip";
import { ENTRA_TYPE_ICON } from "@/components/entra-account";
import type { EntraAgent, UserDetail } from "@/lib/api";
import { ENTRA_LISTS, NO_SPONSOR_CHIP, OBJECT_HINT, SPONSOR_FIX, entraObjectURL, kindWord, opensAccount } from "@/lib/entra-words";
import { entraAccount } from "@/lib/user-look";
import { NONE } from "@/lib/words";

// The parts of the page of an agent that Entra owns: the objects Entra has
// for it, and what Entra lists while the agent has no sponsor.

// AGENT_OBJECTS are an agent's two objects in Entra, in the order the page
// names them, each with its field in the answer.
const AGENT_OBJECTS = [["agentIdentity", "identity"], ["agentUser", "agent_user"]] as const;

// entraObjects are the objects Entra lists for the agent, of every state,
// or only those in state.
export const entraObjects = (entra: EntraAgent | undefined, state?: string) =>
  AGENT_OBJECTS.filter(([, field]) => (state ? entra?.[field] === state : !!entra?.[field])).map(([kind]) => kind);

// ObjectChip is one of an agent's objects in Entra's words. With url it is
// a link that opens the object in the Entra admin centre, and with hint
// its hover says what the object is.
export function ObjectChip({ kind, url, hint }: { kind: string; url?: string; hint?: boolean }) {
  const chip = <Chip tone="plain" icon={ENTRA_TYPE_ICON[kind]} title={hint ? OBJECT_HINT[kind] : undefined} data-entra-object={kind}>{kindWord(kind)}</Chip>;
  if (!url) return chip;
  return <a href={url} target="_blank" rel="noreferrer" className="inline-flex rounded-md hover:opacity-80" data-entra-object-link={kind}>{chip}</a>;
}

// InEntraAs is the Details row of the objects Entra lists for the agent.
// Each chip opens its object in the Entra admin centre. The agent user's
// chip opens nothing when the answer carries no object ID for it.
export function InEntraAs({ user }: { user: UserDetail }) {
  const url = (k: string) => (k === "agentIdentity" ? entraAccount(user) : entraObjectURL("agentUser", user.entra?.agent_user_id || ""));
  return <>{entraObjects(user.entra).map((k) => <ObjectChip key={k} kind={k} url={url(k)} hint />)}</>;
}

// SponsorTag is one sponsor Entra lists, as a tag that opens it in the
// Entra admin centre: by its Straza name when the console found the
// person, else by its object ID. A type the console does not know shows
// as Entra sent it before the ID, with no link.
function SponsorTag({ kind, id, name }: { kind: string; id: string; name: string }) {
  const Icon = ENTRA_TYPE_ICON[kind];
  const url = entraObjectURL(kind, id);
  const text = name || id;
  const tag = (
    <Tag icon={Icon} className={url ? "max-w-full text-link hover:border-link" : "max-w-full"} title={url ? opensAccount(text) : undefined} data-entra-sponsor={id}>
      {!Icon && <span className="text-muted-foreground">{kind}</span>}
      <span className="min-w-0 [overflow-wrap:anywhere] whitespace-normal">{text}</span>
      {url && <ExternalLinkIcon className="size-3 shrink-0 text-link" aria-hidden="true" />}
    </Tag>
  );
  if (!url) return tag;
  return <a href={url} target="_blank" rel="noreferrer" className="inline-flex max-w-full min-w-0 rounded-md align-middle">{tag}</a>;
}

// NoSponsor says why an agent from Entra has no sponsor in Straza: the
// chip, what Entra lists and what to do. names are the Straza names of the
// listed people the console found, by object ID. A state the console does
// not know shows as the server sent it in place of what to do.
export function NoSponsor({ entra, names }: { entra: EntraAgent; names: Record<string, string> }) {
  const listed = entra.sponsors || [];
  const state = entra.sponsor_state || "";
  return (
    <>
      <Chip tone="warn" data-no-sponsor={state}>{NO_SPONSOR_CHIP}</Chip>
      <span className="text-caption text-muted-foreground">{ENTRA_LISTS}</span>
      {listed.length
        ? listed.map((s) => <SponsorTag key={s.id} kind={s.kind} id={s.id} name={names[s.id] || ""} />)
        : <Pill className="border border-dashed border-input bg-transparent px-[9px] py-px italic text-muted-foreground" data-entra-lists-none>{NONE}</Pill>}
      {SPONSOR_FIX[state]
        ? <span className="min-w-0 flex-[1_1_240px] text-foreground" data-sponsor-fix>{SPONSOR_FIX[state]}</span>
        : <span className="min-w-0 flex-[1_1_240px] font-mono text-caption text-muted-foreground" data-sponsor-fix>{state}</span>}
    </>
  );
}
