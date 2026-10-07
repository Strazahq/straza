// The sentences of the editors' two saves and of the origin line. Only
// lazy screens import this file, so
// none of it lands in the entry page.

export const SAVE_DRAFT = "Save draft";
export const SAVE_PUBLISH = "Save and publish";

// Save draft adds the change to the person's working draft and opens it.
export const savedToDraft = (id: string) => "Saved to your draft " + id + ". Nothing changes until you publish it under Drafts.";
export const workingHolds = (id: string, name: string) =>
  "Your draft " + id + " already changes " + name + ". Saving a draft here replaces that change with this one, made on the live version.";

// Save and publish publishes the change on its own draft.
export const liveWithChange = (name: string) => name + " is live with your change.";
export const UNDO = "Undo";
export const publishThis = (name: string) => "Publish this change to " + name + "?";
export const NOTHING_SAVED = "Nothing was saved.";
export const waitsLine = (id: string) => "Draft " + id + " holds the change and waits under Drafts for someone who may publish it.";
// heldLine follows a refusal whose own sentence says what to do next, so
// it states the draft alone. keptLine is the whole note after Cancel.
export const heldLine = (id: string) => "Draft " + id + " holds this change.";
export const keptLine = (id: string) => heldLine(id) + " It waits under Drafts until someone who may publish it does.";
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
    ? "No rule is left in it, so publishing this change removes it, and calls " + role + " makes to " + server + " then run with no approval or deny from it."
    : "No rule is left in it, so your draft removes it when the draft is published, and calls " + role + " makes to " + server + " then run with no approval or deny from it. Until then it stays as it is.";
export const REMOVE_SET = "Remove it";

// The question before Cancel drops a change nobody saved.
export const CLOSE_TITLE = "Close without saving?";
export const CLOSE_BODY = "The changes in this sheet are not stored anywhere. Save draft or Save and publish keeps them, and closing drops them.";
export const CLOSE_DROP = "Close and drop the changes";

// The Add MCP server wizard.
export const publishRow = (name: string) => "Publish " + name;
export const SECRET_WAITS = "Save draft keeps the manifest only. A secret never goes into a draft, so store it on the server's page once the draft is published.";
export const notListed = (name: string) =>
  name + " is published, but the server list did not name it yet, so its secret and check did not run. Open MCP servers, then set the secret on its page.";

// Remove server.
export const removeTitle = (name: string) => "Remove " + name + "?";
export const REMOVE_OPEN = "Remove server…";
export const removedToast = (name: string) => name + " was removed.";

// The origin line on a server's, a role's and a policy's page.
export const originFile = (base: string, dir: string) => "Origin: the file " + base + (dir ? " in " + dir : "") + ".";
export const DIFFERS = "Live differs from the file.";
export const differsSince = (who: string, id: string, at: string) => "Live differs from the file since " + who + " published draft " + id + " at " + at + ".";
export const fileWaits = (id: string) => "The file's newest revision waits as draft " + id + ".";
export const lastPublished = (who: string, at: string, id: string) => "Last published by " + who + " at " + at + ", in draft " + id + ".";
export const openWaits = (n: number, id: string, name: string) =>
  n === 1 ? "Draft " + id + " changes " + name + " and waits under Drafts." : n + " drafts change " + name + " and wait under Drafts.";
export const reviewDraft = (id: string) => "Review draft " + id;
