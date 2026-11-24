// The sentences of the editors' two saves and of the origin line. Only
// lazy screens import this file, so
// none of it lands in the entry page.

export const SAVE_DRAFT = "Save draft";
export const SAVE_PUBLISH = "Save and publish";

// Save draft adds the change to the person's working draft and opens it.
export const savedToDraft = (id: string) => "Saved to your draft " + id + ".";
export const workingHolds = (id: string, name: string) =>
  "Your draft " + id + " already changes " + name + ". Save draft replaces that change.";

// Save and publish publishes the change on its own draft.
export const liveWithChange = (name: string) => name + " is live with your change.";
export const UNDO = "Undo";
export const publishThis = (name: string) => "Publish this change to " + name + "?";
export const NOTHING_SAVED = "Nothing was saved.";
export const waitsLine = (id: string) => "Draft " + id + " holds the change and waits under Drafts for someone who may publish it.";
// heldLine follows a refusal whose own sentence says what to do next, so
// it states the draft alone. keptLine is the whole note after Cancel.
export const heldLine = (id: string) => "Draft " + id + " holds this change.";
export const keptLine = (id: string) => "Draft " + id + " holds this change, under Drafts.";
export const openDraft = (id: string) => "Open draft " + id;
// stillHolds is the line of a save that made a draft of its own because
// the person's earlier draft of the same objects holds another change.
export const stillHolds = (id: string, name: string) =>
  "Draft " + id + " still holds an earlier change to " + name + " that this change leaves out. Publish or discard draft " + id + " under Drafts.";

// The role doors: a role's own set with a saved edit nobody published,
// and the question before a change that leaves the set with no rule.
export const savedEdit = (set: string) => set + " has a saved edit that is not published. Publish or discard it under Drafts, then save again.";
export const removeSetTitle = (set: string) => "Remove " + set + " with this change?";
export const removeSetBody = (role: string, server: string, publish: boolean) =>
  publish
    ? "No rule is left in it, so publishing removes it. Calls from holders of " + role + " to " + server + " then get no approval or deny from it."
    : "No rule is left in it, so your draft removes it when the draft is published. Calls from holders of " + role + " to " + server + " then get no approval or deny from it.";
export const REMOVE_SET = "Remove it";

// The question before Cancel drops a change nobody saved.
export const CLOSE_TITLE = "Close without saving?";
export const CLOSE_BODY = "Your changes are lost.";
export const CLOSE_DROP = "Discard changes";

// The Add MCP server wizard.
export const publishRow = (name: string) => "Publish " + name;
export const checkRow = (name: string) => "Check " + name;
// The doors after the check, which the review page's publish dialog shares
// once it stored a server's secret.
export const STORE_AND_CHECK = "Store and check";
export const SET_AGAIN = "Set it again";
export const RECHECK = "Recheck";
export const finishLater = (name: string) => "Open " + name;
export const SECRET_WAITS = "Save draft keeps no secret. Set it on the server's page after publishing.";
export const notListed = (name: string) =>
  name + " is published but not listed yet. Open MCP servers, then set the secret on its page.";

// ---- the create flows ----

// nextTo names the step the button leads to.
export const nextTo = (step: string) => "Next: " + step;
export const CRUMB_ADD = "Add";
export const CRUMB_NEW = "New";
// The one fold of a raw document, on every create step and change sheet.
export const VIEW_YAML = "View as YAML";
export const APP_YAML_NOTE = "app.yaml, as it will be sent";
export const ROLE_FILE_NOTE = "The role file and the command that sends it";
export const liveNow = (name: string) => name + " is live.";

// Add MCP server.
export const ADD_SERVER_TITLE = "Add MCP server";
export const SERVER_STEPS = ["Server", "Credential", "Review", "Check"];
export const SERVER_CARD = "The server";
export const SERVER_FACT = { name: "Name", description: "Description", runs: "Runs", credential: "Credential", agents: "Agents", tools: "Tools", limits: "Limits" };
// RUNS leads the Runs fact. The address, the command or the image follows in mono.
export const RUNS = {
  remote: "Over HTTP at",
  command: "On the strazad host, as a process Straza starts:",
  oci: "On the strazad host, as a container Straza runs from",
};
// CREDENTIAL_FACT leads the Credential fact. A header or a variable follows in mono.
export const CREDENTIAL_FACT = {
  none: "None.",
  staticHeader: "One shared secret, sent in the",
  staticEnv: "One shared secret, which the process reads from",
  token: "Each person's own token, sent in the",
  oauth: "Each person's own sign-in through",
  header: "header",
};
export const TOOLS_ALL = "Every tool the server lists";
// limitsDefault is the Limits fact of a manifest that sets no limit: the
// server-wide time per call, then rate, the rate limit in rpsWords.
export const limitsDefault = (seconds: number | null, rate: string) =>
  (seconds ? seconds + " s per call." : "The server-wide time per call.") + " " + rate;
export const ACCEPTED = "accepted";
export const REFUSED = "refused";
export const ACCEPTS = "Straza accepts this server.";
export const WHAT_WILL_HAPPEN = "What will happen";
export const NOBODY_UNTIL_ROLE = "Nobody can call it until a role gives access to its tools.";
export const converted = (name: string) => "Converted " + name + " and filled in the fields.";
// The Review verdict while the server has not answered, and when strazad
// is not reached.
export const MANIFEST_PENDING = "Checking the manifest with Straza.";
export const MANIFEST_UNREACHABLE = "Straza is unreachable, so the manifest is not checked here. Save and publish will say.";
export const BACK = "Back";
export const FROM_AGENT_MACHINE = "Call it from an agent's machine, once a role reaches it";

// What next, after a server that runs.
export const liveAndRead = (name: string, tools: number) => name + " is live. Straza reached it and read " + (tools === 1 ? "1 tool" : tools + " tools") + ".";
export const WHAT_NEXT = "What next";
export const NEXT_TILE = {
  role: { title: "Give access to it", line: (tools: number) => "Make an application role that reaches some or all of its " + (tools === 1 ? "1 tool" : tools + " tools") + "." },
  policy: { title: "Write a policy for it", line: "Make calls to its tools need approval, deny them or record them." },
  open: { title: (name: string) => "Open " + name, line: "" },
};

// Remove server.
export const removeTitle = (name: string) => "Remove " + name + "?";
export const REMOVE_OPEN = "Remove server…";
export const removedToast = (name: string) => name + " was removed.";

// The origin line on a server's, a role's and a policy's page.
export const originFile = (base: string, dir: string) => "From " + (dir ? dir.replace(/\/$/, "") + "/" : "") + base;
export const DIFFERS = "Live differs from the file.";
export const differsSince = (who: string, id: string, at: string) => "Live differs from the file since " + who + " published draft " + id + " at " + at + ".";
export const fileWaits = (id: string) => "The file's newest revision waits as draft " + id + ".";
export const lastPublished = (who: string, at: string, id: string) => "Last published by " + who + " at " + at + ", in draft " + id + ".";
export const openWaits = (n: number, id: string, name: string) =>
  n === 1 ? "Draft " + id + " changes " + name + "." : n + " drafts change " + name + " and wait under Drafts.";
export const reviewDraft = (id: string) => "Review draft " + id;
