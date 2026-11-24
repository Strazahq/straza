// The words of the Users list and of a role's Holders tab, which shows the
// same columns. The words the list shares with a user's page are in
// user-words.ts and user-look.ts.
import type { UserKind } from "./user-look";

// The kind tabs above the list.
export const KIND_TABS_LABEL = "Kind of user";
export const KIND_TAB: Record<"all" | UserKind, string> = { all: "All", person: "People", agent: "AI agents", service: "Services" };

// The toolbar.
export const SEARCH_LABEL = "Search users";
export const SEARCH_PLACEHOLDER = "Search by name, email or external id";
export const STATUS_LABEL = "Status";
export const STATUS_OPTION = { all: "any", active: "active", disabled: "disabled" };
export const ROLE_LABEL = "Role";
export const ANY_ROLE = "any role";
export const CLEAR_SPONSOR = "Clear the sponsor filter";

const users = (n: number) => (n === 1 ? "1 user" : n + " users");

// countLine is the muted count beside the filters: the rows that are loaded,
// of the total the server counts for the selected kind tab when it sends one.
export function countLine(loaded: number, total: number | undefined, more: boolean): string {
  if (total !== undefined && loaded < total) return loaded + " of " + users(total);
  return more ? users(loaded) + " shown" : users(loaded);
}

const NONE_OF: Record<UserKind, string> = { person: "No people.", agent: "No AI agents.", service: "No services." };

// emptyKind is what the table says when a kind tab with no other filter
// finds no user.
export const emptyKind = (kind: UserKind) => NONE_OF[kind];

// ALSO_DISABLED opens the hover text of a locked chip on a user who is
// disabled as well, since the Status cell shows one chip.
export const ALSO_DISABLED = "Also disabled.";

// USER_LABELS names the columns, in the headers and in the Columns menu.
// The kind column has no header, so its name shows in the menu only.
export const USER_LABELS: Record<string, string> = {
  kind: "Kind",
  name: "User",
  sponsor: "Sponsor",
  roles: "Roles",
  source: "Source",
  last_seen: "Last seen",
  status: "Status",
  created: "Created",
};
