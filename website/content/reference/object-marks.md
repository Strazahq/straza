---
title: Object marks for server authors
description: The two tool marks an MCP server sends so Straza can limit a role to some of the server's objects, the listing tool's answer, the listing credential, what Straza sends on each call and what the server promises.
pagetype: reference
weight: 65
draft: false
keywords: object marks ai.straza/object ai.straza/lists-objects ai.straza/object-id listing credential server author covers
---


Read this page when you write an MCP server that reaches many separate objects with one credential, such as every repository of an organization, and its operators want to limit a role to some of them. Without marks, Straza can only give a role a tool, so a role that may call your write tool may call it on every object your credential reaches. With marks, your tool list says which argument names the object and which tool lists the objects. Straza then checks each call against the objects the caller's roles cover before your server sees it. When the role that allows a call covers only some objects, Straza also tells your server which object it checked. You need no Straza library: two `_meta` keys in your `tools/list` answer, one listing tool, and the checks at the end of this page.


The marks change nothing by themselves. An operator reads them with `strazactl apps objects` and pins them in your server's manifest under `straza.objects`, through a draft that a person publishes. From then on Straza decides with that pinned copy and lists your objects in the background. Each role on your server then says which objects it covers: `covers: every`, or `covers: {objects: [...]}` with values your listing returned. [Add a server]({{< relref "guides/serve-mcp-apps/add-a-server.md#objects" >}}) is the operator's side of the same steps. The examples on this page come from ghapp, a GitHub App server that reaches the repositories of an organization, with the object type `repo`. The enterprise demo stack runs a smaller server of the same shape, [objects-demo](https://github.com/strazahq/straza/tree/main/test/objectsdemo), written in Go.

## The two marks {#marks}


Put the marks in each tool's `_meta`, under Straza's own prefix `ai.straza/`, as the MCP specification asks of extensions. This example, trimmed from ghapp's `tools/list` answer, marks a tool that works on one repository and the listing tool, which carries both marks:

```json
{ "tools": [
  { "name": "pr_create",
    "inputSchema": { "type": "object",
      "properties": { "repo": { "type": "string" }, "title": { "type": "string" },
                      "head": { "type": "string" }, "base": { "type": "string" } },
      "required": ["repo", "title", "head", "base"] },
    "_meta": { "ai.straza/object": { "type": "repo", "argument": "repo" } } },
  { "name": "repo_list",
    "annotations": { "readOnlyHint": true },
    "inputSchema": { "type": "object", "properties": { "cursor": { "type": "string" } } },
    "_meta": { "ai.straza/object": { "type": "repo", "reach": "all" },
               "ai.straza/lists-objects": { "type": "repo",
                 "attributes": [ { "name": "team", "setBy": "org" } ] } } }
] }
```

Every other ghapp tool, its searches included, names the repository in a required `repo` argument and carries the same mark as `pr_create`.

`ai.straza/object` says how one tool reaches objects:

| Field | Required | Value |
|---|---|---|
| `type` | yes | The object type, one lowercase word of up to 32 letters, digits or hyphens that starts with a letter, such as `repo`. |
| `argument` | one of `argument` and `reach` | The name of the argument that carries the object, for a tool whose call names one object. |
| `reach` | one of `argument` and `reach` | `all` for a tool that can reach every object at once, such as a search across them. `none` for a tool that touches no object, such as one that says who the caller is. `credential` for a tool whose call does not name the object, so the credential the call carries limits what it reaches. |

`ai.straza/lists-objects` goes on the one tool that lists the objects:

| Field | Required | Value |
|---|---|---|
| `type` | yes | The same type as every other mark of the server. |
| `attributes` | no | Up to 16 attributes your listing returns for each object, each with a `name` and a `setBy`. `setBy` is `org` when only the organization's admins can change the value in your system, and `object` when the object's own admins can change it too. A missing `setBy` reads as `object`. |


Once an operator pins your marks, the reach of a tool decides which roles may call it. A server that works on one object per call needs only `argument`. The other reaches are for tools that do not name one object: a search across every object reaches `all`, a tool that says who the caller is touches `none`, and a tool whose call never names its object, so that the credential the call carries is the only limit, reaches `credential`.

| Reach | Who may call the tool |
|---|---|
| `argument` | A role whose covers line covers the object the argument names. Straza looks the value up byte for byte in its list, so a role that names objects one by one is refused a value the list does not hold. |
| `none` | Any role that may call the tool, whatever objects it covers. |
| `all` | Only a role on every object, because nothing keeps the call to some of them. |
| `credential` | Only a role on every object today, because Straza does not yet check what a role's own credential reaches. |
| no mark | Treated as `all` until a person decides, so a tool you add later never slips past a limit. |

The listing tool lists every object by nature, so its reach is `all`. Mark it `all`, as ghapp does, or leave its `ai.straza/object` mark out. Straza refuses any other reach for it, in your marks and in a pinned block alike, and `strazactl apps objects` names the fault: `ghapp's marks break a manifest rule: objects.tools.repo_list gives the listing tool the reach none. The listing tool returns every repo, so its reach is all. Write all, or leave repo_list out of objects.tools.` A draft that puts a tool of reach `all` or `credential` in a role on some objects is refused before anyone can publish it.

## Rules for a valid mark {#rules}


Straza reads your marks from your server's live tool list. While the server runs, the check of a draft that pins them compares the pinned copy with that list. A server's marks are valid when all of these hold:

- Each `ai.straza/object` mark carries a `type` and exactly one of `argument` or `reach`, never both. `reach` is `all`, `none` or `credential`.
- Every mark of the server names the same type, because a server has one object type.
- Exactly one tool carries `ai.straza/lists-objects`, and that tool sets `readOnlyHint: true` in its annotations.
- An argument a mark names is a top-level property of the tool's `inputSchema` whose `type` is the string `"string"`, and the schema lists it in `required`. Its name is up to 64 letters, digits, `_` or `-`, and it starts with a letter or `_`.
- Tool names follow the MCP specification: 1 to 128 ASCII letters, digits, `_`, `-` or `.`.
- Attribute names are up to 64 letters, digits, `_`, `.` or `-` that start with a letter, and each appears once.

Nested paths, arrays, numbers, optional arguments and an object named by two arguments together, such as an owner and a repository name, are outside these rules. Give such a tool one required string argument that names the object whole, such as `Strazahq/pay-api`, or mark it `all`.


On a running server that pins nothing, `strazactl apps objects` prints the block your marks declare and a sentence for each fault it finds. Were one of ghapp's tools to mark the type `project`, the sentence would be "ghapp's tools mark 2 object types: project, repo. A server has one object type, so its author must mark every tool with the same type." Until the marks are fixed, `strazactl apps objects draft` refuses to open a draft. The check of a draft that pins the block refuses a pinned argument that is not a required string of its tool, a listing tool that is not marked read-only, and a pinned reach looser than your own mark. It can compare only while your server runs and lists its tools. Otherwise the check carries a line that says nothing was compared, which does not block the publish, and the gate still refuses what the pinned block does not allow.

## The listing {#listing}


Straza calls your listing tool itself, with an ordinary `tools/call`. The arguments are `{}` for the first page and `{"cursor": "..."}` with your last `nextCursor` for each page after, so declare `cursor` as an optional string in the tool's input schema. Answer each page in `structuredContent`. Straza reads only that and `isError`, so any text content is for people. An example page in ghapp's shape, whose cursor is opaque to Straza:

```json
{ "objects": [
    { "value": "Strazahq/pay-api", "id": "88112070", "title": "pay-api",
      "attributes": { "team": "payments" } },
    { "value": "Strazahq/site", "id": "90123310", "title": "site",
      "attributes": { "team": ["web", "docs"] } } ],
  "nextCursor": "eyJpIjo1MTIzNDU2NywicCI6Mn0" }
```

| Field | Rule |
|---|---|
| `value` | Required. The exact string a tool argument carries for this object: a non-empty string of valid UTF-8 of at most 512 bytes. Straza compares it byte for byte, with no trimming, case folding or URL decoding. |
| `id` | A non-empty string, or a whole number from 0 to 2^53, that never changes and is never reused, so an object deleted and made again under the same value gets a new id. At most 512 bytes, and Straza keeps a number as its decimal string. An item with no id uses its value as the id, so a deleted and recreated object then looks like the old one. |
| `title` | The name people read, a string of at most 256 bytes. |
| `attributes` | At most 16 per object, and that count includes every attribute you send. Straza keeps only the attributes the pinned block names and shows them with each object. Each kept attribute is a string of at most 128 bytes or a list of at most 16 such strings, and Straza does not read the others. |
| `nextCursor` | A string. Absent or empty ends the listing. Sending back the cursor you were called with fails the listing, because it would never end. |


A listing counts only when every page arrived. An item that repeats an earlier item of the same listing exactly is dropped, because a server that pages by number shifts its pages when an object is created mid-listing. One value with two ids, one id with two values, or a repeat with another title or other kept attributes fails the listing. A listing may hold 100,000 objects, each page has 30 seconds, and the whole listing has 5 minutes.

A listing that breaks a rule or a limit, answers `isError: true`, or runs out of time fails as a whole and changes nothing. Straza keeps the last good list and goes on deciding with it. The reason shows on the server's tab in the console, in `strazactl apps objects`, and in the `apps.objects.list` audit record, which Straza writes when a listing changes the list or its outcome flips. The reasons that point at your listing tool read, for a tool named `repo_list`:


| Your listing tool | The operator reads |
|---|---|
| lacks `readOnlyHint: true` | `repo_list is not marked read-only, so Straza will not call it to list repos. The server's author must set readOnlyHint on it.` |
| answers without `objects` in `structuredContent` | `repo_list answered without an objects list in its structured content. The server's author must return {objects, nextCursor} as the Straza object marks describe.` |
| returns the cursor it was called with | `repo_list answered with the cursor it was called with as nextCursor, so the listing would never end. The server's author must return a new nextCursor, or none on the last page.` |
| breaks a limit | A sentence that names the limit, such as `repo_list returned more than 100,000 repos, so the listing was refused as a whole and the last good list stays.` |
| answers `isError: true` | `repo_list answered an error:`, the first line of your error text, at most 160 bytes with anything shaped like a credential masked, then `. Check ghapp's logs on its Activity tab.` |
| takes more than 30 seconds for a page | `repo_list did not answer within 30 seconds on page 2.` |


Straza lists your server every 10 minutes. It also lists within about 15 seconds after a person publishes a pin, changes its type, listing tool or attributes, or changes the listing credential. A change of the type, the listing tool or the attributes starts the list over empty, so until the next good listing there is no last good list, and roles that name objects are refused while roles on every object keep working. When the same publish starts or restarts your server, that first listing can fail. While the server is still starting, the reason reads `ghapp is not running, so Straza could not call repo_list. Start it, then refresh.` Once it runs but Straza has not read its tool list yet, the reason reads `Straza has not read ghapp's tool list yet, so it cannot check repo_list before it lists ghapp's repos. It tries again in about a minute. If ghapp stays degraded, read why with strazactl apps show ghapp.` Both are tried again in about a minute, and a server that failed to start, which can give either reason, is tried again each minute until it runs. Any other failed listing is tried again 10 minutes later, and so is the listing of a server an operator stopped or paused. A refused call that names a value the list does not hold asks for a listing sooner, and so does an operator's `strazactl apps objects refresh`, but neither runs within a minute of the last listing. One replica of a deployment lists at a time, so expect one full listing every 10 minutes per deployment and at most one more each minute.

## The listing credential {#listing-credential}


On a `remote` server whose manifest names a credential, Straza calls your listing tool with a listing credential: a secret the operator stores for that one purpose with `strazactl apps secret set <server> --listing`. It arrives where every other credential does, in the header your manifest's `credential.inject` names. Straza sends it on the listing call only. It never falls back to another secret, such as your server's own, so without it the listing fails with "ghapp has no listing credential, so Straza cannot list its repos. Add one with strazactl apps secret set ghapp --listing, then refresh with strazactl apps objects refresh ghapp." A remote server that takes no credential is listed without one. A `command` or `oci` server is one process with one identity, so Straza lists through that process with no credential per call.


Accept the listing credential on the listing tool only, and refuse it on every other tool. No person stands behind it, so a listing credential that opened your write tools would be a way around every role. Offer operators a credential that can read the list and do nothing else. An agent's call of your listing tool through Straza carries the credential Straza uses for that agent's other calls, never the listing credential. The listing tool's reach is `all`, so only a role on every object may make that call. objects-demo shows the split: its listing tool answers only to its listing token, its repository and search tools refuse that token, and its `whoami` tool only says which token a call carried.

## What reaches your server on a call {#calls}


Once your marks are pinned, Straza checks each call to your server's tools before any policy runs, and a call it refuses never reaches your server. For a tool of reach `argument`, it first refuses arguments that two JSON readers could read differently, such as bytes that are not valid UTF-8, a key that appears twice, or a second key that differs from your argument's name only in case. It also refuses a call whose argument is missing or is not a string. Straza never rewrites the object argument, so your server receives the value the check read, as the agent sent it.


When the role that lets a call through names objects one by one, Straza adds the object's id from its list to the call's `_meta`, as in this example:

```json
{ "jsonrpc": "2.0", "id": 7, "method": "tools/call",
  "params": { "name": "pr_create",
    "arguments": { "repo": "Strazahq/pay-api", "title": "Fix the build",
                   "head": "fix-build", "base": "main" },
    "_meta": { "ai.straza/object-id": "88112070" } } }
```

The id is always a string, the decimal form when your listing sent a number. Compare it with the id your system holds now for the value the call names, and refuse the call when they differ. A different id means the object was deleted and made again under the same value after Straza listed it, and the role covered the old one. If your server mints a narrower credential for each call, mint it for the id Straza sent, not for an id you resolved from the value, so a rename between the check and the call still leaves the credential on the object Straza checked. A call through a role on every object carries no id, and your server resolves the value itself. Straza sends no id on a tool whose reach is not `argument`, and none on the listing call.

## The three promises {#promises}


Every mark carries three promises that Straza cannot check. When you mark a tool you make them, and an operator who pins your marks relies on them:

1. A call touches only the object its argument names.
2. A value names exactly one object at the time of the call, with no rename redirect, alias or case folding. Match the value byte for byte, as Straza does.
3. Your server accepts calls only from Straza's gateway, or checks a credential that only the gateway can obtain.

Straza checks objects only in its MCP gateway, and its harness hooks never run this check. A call that reaches your server some other way carries whatever arguments and `_meta` its sender chose, so the first two promises hold only while the third does.

## What you can rely on {#rely}


- Marks only pre-fill a draft. Straza decides with the copy a person pinned under `straza.objects`, never with your live marks.
- Whoever pins your marks may make a reach stricter and never looser. `all` is stricter than every other reach, and every reach is stricter than `none`.
- A changed mark is drift that a person resolves. Straza counts the `ai.straza/*` keys in the tool inventory it watches, so a change records `straza.apps.drift` as a changed description or schema does, and `strazactl apps objects` tells the operator when a draft from your marks would change the pinned copy.
- Until a person publishes a new pin, a tool is refused to roles on some objects when its live mark is now stricter than its pinned reach, names another type or no longer parses, or when its live input schema no longer lists the pinned argument as a required top-level string. Roles on every object keep calling it. Removing a mark leaves the pinned reach as it was.
- A tool you add without a mark reads as `all` until a person names its reach.
- Straza reads only the name and the arguments of an agent's `tools/call`, so `_meta` an agent sends never reaches your server, and `ai.straza/object-id` always comes from Straza.
- Your marks are not secret. The combined `/mcp` endpoint drops tool `_meta`, but a server's own endpoint, `/mcp/` followed by its name, serves your tools' `_meta` as you sent it.
- An operator can turn every listing off with `apps.objects.enabled: false`. A tool that names an object then answers only roles on every object, so turning it off never widens access.

## A server that also serves git {#git}


A server that holds a git host's credential, as ghapp holds a GitHub App's key, can also serve git, so that an agent runs plain git and the credential never reaches the agent's machine. Give the server one tool for a fetch and one for a push, each marked with the argument that names the repository, and serve git's smart HTTP protocol under your public address followed by `/git/`. An operator maps the two tools in the manifest's `straza.git` block, and Straza refuses that block unless the objects block pins each mapped tool to an argument. Straza decides each git operation as a call of the mapped tool with the repository in that argument, then issues a short git grant that your server verifies on every request, together with a proof from the agent machine's key. [The git grant specification](https://github.com/strazahq/straza/blob/main/spec/grant/SPEC.md) states the claims, the binding and the order of the checks, and its examples replay in your own tests. [Git through Straza]({{< relref "guides/serve-mcp-apps/git-through-straza.md" >}}) is the operator's side.

## Before you tell operators it is ready {#checklist}


Run through this list against a Straza you control, such as the standalone profile on your own machine:

1. Every tool that acts on one object carries `ai.straza/object` with its `argument`, and every other tool but the listing tool carries a `reach`, so no tool waits for a decision.
2. The argument each mark names is a required top-level string of its tool's input schema.
3. One tool carries `ai.straza/lists-objects` and `readOnlyHint: true`, and all your marks name the same type.
4. On the running server, `strazactl apps objects <server>` prints the block your marks declare and no fault sentence.
5. Once you pin the block, and store the listing credential when your server takes one, Straza lists the server within about 15 seconds. `strazactl apps objects <server>` then shows every object with the id and title you expect, and no failure reason. If that first listing failed because the server was still starting, Straza lists it again in about a minute, with no refresh.
6. An object deleted and made again under the same value gets a new id, and every listing stays within the limits above.
7. Your listing tool answers to the listing credential, and every other tool refuses it.
8. A tool that names an object refuses a call whose `ai.straza/object-id` differs from the object's id now, and accepts a call that carries no id.
9. Values match byte for byte, as the second promise asks.
10. Your server is reachable only from Straza's gateway, or checks a credential only the gateway holds.
