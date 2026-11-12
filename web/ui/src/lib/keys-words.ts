// The words of the Agent signing keys tab, its Overview lines and an
// agent's Entra link on the user sheet. A key is named by the first eight
// characters of its id everywhere, the full id on hover. Words that take
// blueprints get the client ids the providers name: an empty list when no
// provider uses Entra, and null when the providers could not be read, which
// keeps the Entra words with their hedges.
import { absTime } from "./words";

export const shortKid = (kid: string) => kid.slice(0, 8);
const dateOf = (iso: string) => absTime(iso).slice(0, 10);
const days = (n: number) => n + (n === 1 ? " day" : " days");

// ---- the tab ----

export const SUBJECT_KEYS = "The signing keys";
export const READING_KEYS = "Reading the signing keys…";
export const keysLede = (entra: boolean) => entra
  ? "Straza signs with the active key when it gets a token for an agent. Keycloak and Okta read a new key from Straza by themselves. Entra needs the new key's certificate on the agent blueprint before you promote it."
  : "Straza signs with the active key when it gets a token for an agent from your identity provider. The provider reads a new key from Straza by itself, so you rotate, then promote the new key a minute later.";
export const KEYS_EMPTY_TITLE = "No key yet";
export const keysEmptyBody = (blueprints: string[] | null) =>
  blueprints === null ? "Create one, upload its certificate to the Entra blueprint if your agents use Entra, then promote it."
  : blueprints.length ? "Create one, upload its certificate to the Entra blueprint, then promote it."
  : "Create one, then promote it a minute later.";
export const hiddenKeysTabLine = (label: string) => label + " needs a full administrator.";
export const keysCount = (n: number) => n + (n === 1 ? " key" : " keys");
export const KEY_COLUMN = { kid: "Key", status: "Status", created: "Created", until: "Certificate until" };

export const ROTATE_KEY = "Rotate key";
export const CREATE_KEY = "Create key";
export const ROTATE_TITLE = "Rotate the key?";
export const CREATE_TITLE = "Create the key?";
export const rotateBody = (active: string) => shortKid(active) + " keeps signing until you promote the new one.";
export const CREATE_BODY = "Straza creates a key. It signs nothing until you promote it.";
export const CREATE_NEW_KEY = "Create new key";
export const stagedToast = (kid: string) => "Key " + shortKid(kid) + " staged.";

export const CERTIFICATE = "Certificate";
export const certFile = (kid: string) => "straza-client-assertion-" + shortKid(kid) + ".pem";
export const downloadedToast = (kid: string) => "Downloaded " + certFile(kid) + ".";
export const RETIRE = "Retire";
export const retireTitle = (kid: string) => "Retire " + shortKid(kid) + "?";
export const RETIRE_ACTIVE_BODY = "Only for a key that may have been copied. Agent sign-ins fail until you rotate and promote a new key.";
export const RETIRE_STAGED_BODY = "This abandons the rotation. The active key keeps signing.";
export const RETIRE_ENTRA = "Entra accepts it until you remove its certificate from the blueprint.";
export const retiredToast = (kid: string) => shortKid(kid) + " is retired.";
export const showRetired = (n: number) => "Show " + n + " retired " + (n === 1 ? "key" : "keys");
export const HIDE_RETIRED = "Hide retired keys";
export const RETIRED_LINE = "Remove a retired key's certificate from the Entra blueprint if it is still there.";

// ---- the rotation panel ----

export const ROTATING = "Rotation in progress";
export const STEP_STAGED = "New key staged";
export const stagedLine = (staged: string, active: string | null) =>
  shortKid(staged) + " is published." + (active ? " " + shortKid(active) + " still signs." : "");
export const STEP_UPLOAD = "Upload its certificate to the Entra blueprint";
export const uploadLine = (blueprints: string[] | null) => blueprints && blueprints.length
  ? "Entra admin centre › Agent blueprints › " + (blueprints.length > 1 ? "each blueprint below" : "the blueprint below") + " › Credentials › Certificates. Needs the Agent ID Administrator role."
  : "Entra admin centre › Agent blueprints › your blueprint › Credentials › Certificates. Needs the Agent ID Administrator role. Skip if no agent uses Entra.";
export const BLUEPRINT = "Blueprint";
export const BLUEPRINT_COPIED = "Blueprint id copied.";
export const DOWNLOAD_CERT = "Download certificate";
export const ENTRA_LISTS = "Entra lists it as";
export const COPY = "Copy";
export const THUMB_COPIED = "Thumbprint copied.";
export const STEP_PROMOTE = "Promote the key";
export const promoteWait = (s: number) => "Available in " + s + " seconds, once every replica has the key.";
export const PROMOTE_READY = "Ready.";
export const PROMOTE = "Promote";
export const promoteTitle = (kid: string) => "Promote " + shortKid(kid) + "?";
export const PROMOTE_BODY = "Entra refuses every agent until the blueprint holds this key's certificate.";
export const promotePlainBody = (active: string | null) =>
  "Every replica signs with it within 30 seconds" + (active ? ", and " + shortKid(active) + " retires by itself." : ".");
export const promoteTick = (thumb: string | null, blueprints: string[] | null) =>
  (blueprints && blueprints.length > 1 ? "Each blueprint " : "The blueprint ") +
  (thumb ? "lists thumbprint " + thumb : "holds this key's certificate") +
  (blueprints && blueprints.length ? "." : ", or no agent uses Entra.");
export const promotedToast = (kid: string) => shortKid(kid) + " is the active key.";
export const promotedTitle = (kid: string) => shortKid(kid) + " is active";
export const STEP_PROMOTED = "Promoted";
export const PROMOTED_LINE = "All replicas sign with it within 30 seconds.";
export const removeOldTitle = (blueprints: string[] | null) =>
  "Remove the old certificate from " + (blueprints && blueprints.length > 1 ? "each blueprint" : "the blueprint");
export const retiresLine = (kid: string, seconds: number | null) =>
  shortKid(kid) + (seconds ? " retires in " + seconds + " seconds." : " retires within 2 minutes.");
export const removeOldLine = (kid: string, seconds: number | null) =>
  retiresLine(kid, seconds) + " Entra accepts its certificate until you remove it from the blueprint.";
export const MARK_REMOVED = "Mark as removed";
export const MARKED_REMOVED = "Marked as removed.";
export const STEP_LINK = "Link each agent to its Entra agent identity";
export const LINK_LINE = "Your identity manager sends entraAgentId, or an administrator runs strazactl users entra-agent-id set. Each agent's page under Users shows its link.";
export const OPEN_USERS = "Open Users";

// ---- the Overview lines ----

export const certExpiredLine = (kid: string, at: string) =>
  "The certificate of signing key " + shortKid(kid) + " expired on " + dateOf(at) + ". Agents get no Entra tokens until you rotate the key.";
export const certExpiringLine = (kid: string, left: number, at: string) =>
  "The certificate of signing key " + shortKid(kid) + " expires in " + days(left) + ", on " + dateOf(at) + ". Rotate the key before then.";
export const stagedWaitingLine = (kid: string, waited: number) =>
  "Signing key " + shortKid(kid) + " has been staged for " + days(waited) + " and signs nothing. Promote it or retire it.";

// ---- an agent's Entra link ----

export const ENTRA_SECTION = "Entra agent identity";
export const OWNER_STRAZA = "Owner: Straza";
export const OWNER_IDM = "Owner: identity manager";
export const NOT_LINKED = "Not linked. Calls to servers that trust Entra are refused.";
export const LINKED_BY_IDM = "Set by your identity manager. Change it there.";
export const LINK_BY_IDM = "Your identity manager sets it when it maps entraAgentId.";
export const LINKED_BY_ADMIN = "Set by an administrator with strazactl users entra-agent-id set.";
export const linkByAdmin = (name: string) =>
  "Your identity manager sends entraAgentId, or an administrator runs strazactl users entra-agent-id set " + name + " <client id>.";
export const NO_TYPE = "This record has no type agent, so it can't be linked. Your identity manager sets the type, or an administrator through the admin API.";
export const ID_COPIED = "Client id copied.";
