---
title: Limit a role to some objects
description: Pin the repositories ghapp reaches, give Straza its listing credential, then give each role the repositories it covers, so an agent with write tools writes only where one of its roles allows.
summary: >-
  On ghapp, a role with `pr_create` could open pull requests in every repository the App
  reaches. You pin ghapp's repositories from its page, give Straza a listing credential so it
  lists them, and give each role the repositories it covers. The gateway then refuses a call
  whose `repo` no role of the caller covers.
pagetype: how-to
weight: 32
draft: false
tested:
  version: v1.0.0-1561-g98d49634
  platform: The enterprise demo stack on Rocky Linux 10.1, with ghapp started beside it from its own deploy kit on a private GitHub App installed on the organization ghapp-straza-test. Every strazactl step but the two publishes ran on an admin API token, and the note strazactl then printed on stderr on a token is left out of the output. Both drafts were then published on the admin's own strazactl login, with y answered at the prompt. strazactl no longer signs in or publishes, so a person publishes each draft in the console, and the last line of each draft's output, which names that way, was written from the code on 2026-11-01 and not run. The warning line of the pin's draft was written from the code on 2026-11-05 and not run. Both secrets were set from STRAZA_SECRET_VALUE, so no hidden prompt shows. The SCIM read ran on a token with scim:read alone. midPoint had imported both roles before the assignment loop ran, so it did not wait. The four refusals in the table were checked with strazactl drafts check on edited copies of the two role files. The agent's calls ran as joe-java-developer-agent through straza mcp ghapp in the demo stack's harness container, a Debian 12 machine with straza v1.1.0-138-g8cd72d69. The console steps were read from the console's source and not clicked, and the console's pin and listing credential steps were written on 2026-11-05 from the console's code in the working tree, before it shipped, and not clicked. A fix then changed three sentences, so the second and fourth rows of the refusal table, the agent's calls and the audit records were walked again on 2026-10-20 at v1.0.0-1561-g98d49634. The other steps ran on 2026-10-19 at v1.0.0-1556-ge9afd09a. The console's Pin and listing credential steps were walked on 2026-11-05 at v1.0.0-1746-gdfac0fc2 on the same stack with objwalk, a scratch copy of objects-demo with nothing pinned, rather than with ghapp.
  except: the lines of each draft's output written from the code
  date: 2026-10-20
applies_to: both
keywords: object scope covers repos repositories pin listing credential generate ghapp github app objects draft scim midpoint strazaCovers refusal
who: Admin, and an agent that drafts the change
where: Console or strazactl, your midPoint, and the agent's machine
steps: true
modes: [console, cli]
mode_default: cli
---


An MCP server such as ghapp marks each of its tools with the object it works on, here a GitHub repository. Pinning turns on Straza's checks for that server and saves a copy of those marks, and Straza decides with that copy from then on. The listing credential is an extra token that lets Straza read the names of the server's objects, so you can pick them for a role.


ghapp reaches every repository its GitHub App is installed on. A role with `pr_create` could open pull requests in all of them. This page pins ghapp's repositories, gives Straza the listing credential and gives each role the repositories it covers. You end with a role that writes in two repositories, a role that reads in every one, an agent in midPoint that holds both, and a refused call on a repository the write role does not cover.


ghapp marks its own tools, so you write no declaration by hand. Each of its 14 repository tools, such as `repo_get`, `pr_create` and `pr_merge`, names its repository in one required argument, `repo`, as owner/name. With git transport on, `git_fetch` and `git_push` name theirs the same way. Its listing tool, `repo_list`, returns every repository the App reaches with GitHub's id and the custom properties you ask for, here `team`. Because `repo_list` reaches every repository at once, only a role on every repository may hold it.

## Before you start {.nostep}


- ghapp running where strazad can reach it, started with `--http` on that address, with its App id and private key. The App on this page is installed on the organization `ghapp-straza-test`, whose repositories carry the custom property `team`, and ghapp starts with `GHAPP_MCP_ATTRIBUTES=team` so its listing reports it.
- Two random values in ghapp's config. `GHAPP_MCP_GATEWAY_SECRET` is the server's own secret, which Straza sends on every tool call. `GHAPP_MCP_LISTING_TOKEN` opens `repo_list` alone and becomes the listing credential. The console can make this second value for you, and ghapp reads it when it starts.
- A Straza global admin to add ghapp. From the pin to the roles, ghapp's own admin may do every Straza step in the console. For the strazactl steps, an admin API token in `STRAZA_API_TOKEN` that holds `apps:read`, `apps:write`, `drafts:read` and `drafts:write`, minted in the console as [Mint an admin API token]({{< relref "guides/console/settings.md#mint-an-admin-api-token" >}}) shows. Reading the audit log needs a global admin or an admin role whose areas include `audit:read`, as [Delegated admin]({{< relref "guides/operate/delegated-admin.md" >}}) sets up.
- midPoint connected to Straza as [midPoint]({{< relref "guides/connect-identity/midpoint.md" >}}) sets it up, an admin API token with the scope `scim:read`, and a midPoint admin login for the assignment. Any SCIM client reads the same `covers` values.
- An AI agent that calls through Straza's gateway, such as `joe-java-developer-agent` of the demo stack.

## Add ghapp


ghapp is a remote server that reads a bearer token, and Straza sends `GHAPP_MCP_GATEWAY_SECRET` as the server's own secret on every tool call. The address `http://ghapp:3003/mcp` is ghapp's name and port on the demo stack's Compose network, so put the address strazad reaches ghapp at in its place.

{{< console >}}
{{< clicks "MCP servers" "Add MCP server" >}}

On Server, type `ghapp`, pick **A server you run, over HTTP** and enter ghapp's address. The Credential step comes next: pick **One shared secret for this server**, type the value of `GHAPP_MCP_GATEWAY_SECRET`, and leave the way it is sent on `A bearer token in Authorization`. Press **Save and publish** on Review. [Add a server]({{< relref "guides/serve-mcp-apps/add-a-server.md" >}}) walks each step of the wizard.
{{< /console >}}

{{< cli >}}
{{< command terminal="Your terminal" purpose="administration" >}}
```sh
cat > ghapp.yaml <<'EOF'
apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: ghapp
  namespace: ai.straza
  description: GitHub through one GitHub App, a token per call for one repo
server:
  name: ai.straza/ghapp
  description: Repos, pull requests and issues on GitHub. Each call gets a GitHub token for the one repo it names.
  repository:
    url: https://github.com/strazahq/ghapp-mcp-server
    source: github
  version: "0.0.1"
straza:
  runtime:
    kind: remote
    remote:
      url: http://ghapp:3003/mcp
  credential:
    kind: static
    inject:
      as: header
      name: Authorization
      template: "Bearer {{secret}}"
  exposure:
    tools: ["*"]
  limits:
    rps: 10
EOF
strazactl spec validate app -f ghapp.yaml
strazactl apps install -f ghapp.yaml
strazactl apps secret set ghapp
```
{{< /command >}}

```text
ghapp.yaml: app OK
installed ghapp (remote runtime). Health: degraded, requires a credential and none is stored. Set one with strazactl apps secret set ghapp, which asks for the value at a hidden prompt.
secret set for the MCP server ghapp: the server's own secret, fingerprint ae0f
```

`strazactl apps secret set ghapp` asks for the server's own secret at a hidden prompt. Type the value of `GHAPP_MCP_GATEWAY_SECRET` there.
{{< /cli >}}

## Pin ghapp's repositories


Pinning saves a copy of ghapp's marks in its manifest. It goes through a draft, like every change to a live server, and nobody's access changes when you pin, because a role made before the pin keeps every repository. A mark that ghapp changes later waits until a person pins again.

{{< console >}}

{{< clicks "MCP servers" "ghapp" "Overview" "Pin repos" >}}

While ghapp's tools name repositories and nothing is pinned, Overview opens with a card that reads `ghapp's tools work on repos. Pin them so roles can pick repos and Straza can list them.` Its first step, `Pin repos. You see what gets pinned before anything changes.`, carries the button **Pin repos**. Its second step waits for the first.

The dialog `Pin repos on ghapp?` says what pinning does: `Straza will check every call against the repos the caller's role covers. Nobody's access changes when you pin: roles made before keep every repo.` Under it, **Object type** reads `repo (repos)`, **Read from** reads `the repo argument, on 14 of 15 tools`, and **Listed by** reads `repo_list, which reaches every repo`. The line `All of it comes from ghapp's own tool marks.` carries a help icon that says what happens when ghapp changes a mark later.

Press **Save draft** to open the draft's page before anything goes live, or **Pin and publish** to make the draft and publish it in one step, as **Save and publish** does in the other editors. Either way the draft is checked and recorded. A draft with nothing to read publishes at once, with the toast `Repos are pinned on ghapp.`, and a warning, such as the one below, opens the publish dialog first. The draft is titled `Change server ghapp`. Its card shows the pinned type and the tool marks, now and after publishing, and ends `No other field changes.` After **Save draft**, publish it with **Publish…** in the band `Before you publish`.

{{< fails >}}
`ghapp is not running, so Straza cannot read its tool marks. Start it, then draft again.`
: The dialog shows ghapp's own sentence in place of its buttons, and it does the same for a broken mark. Start ghapp, or fix the mark on its side, then press **Pin repos** again.
{{< /fails >}}
{{< /console >}}

{{< cli >}}

{{< command terminal="Your terminal" purpose="administration" >}}
```sh
strazactl apps objects ghapp
strazactl apps objects draft ghapp
```
{{< /command >}}

```text
ghapp pins no object type. Its tool marks declare this block:
straza:
  objects:
    type: repo
    list:
      tool: repo_list
      attributes:
        - name: team
          setBy: org
    tools:
      branch_list:
        argument: repo
      file_get:
        argument: repo
      issue_comment:
        argument: repo
      issue_create:
        argument: repo
      issue_get:
        argument: repo
      issue_list:
        argument: repo
      pr_comment:
        argument: repo
      pr_create:
        argument: repo
      pr_get:
        argument: repo
      pr_list:
        argument: repo
      pr_merge:
        argument: repo
      repo_get:
        argument: repo
      repo_list: all
      search_code:
        argument: repo
      search_issues:
        argument: repo
Pin it with strazactl apps objects draft ghapp, then publish the draft.
Created draft 39 with 1 document. Nothing changes until a person publishes it.
  ~   App/ghapp
  warning   App/ghapp ghapp has no listing credential, so Straza cannot list its repos, and only roles on every repo can use its repo tools until it has one. After you publish, set one under Server authentication on the Overview of ghapp, or with strazactl apps secret set ghapp --listing.
Publish draft 39 in the console under Drafts: http://localhost:8420/console/drafts/39
```

The first command prints the `straza.objects` block that ghapp's marks declare. The second opens a draft of ghapp's manifest with that block. strazactl runs on an admin API token, and a token never publishes, so a person publishes the draft in the console under **Drafts**.
{{< /cli >}}


The draft's check compares the copy with ghapp's live tools. `repo_list` must be marked read-only, and `repo` must be a required string argument of every tool that names it. Until the next step, the check warns that ghapp has no listing credential yet, and that warning does not stop the publish. When ghapp already has roles, the first pin gives each of them `covers: every`, and the check says so, such as `Pinning repos on ghapp keeps today's reach: ghapp-devs now covers every repo. Narrow it in this draft or later.`

{{< details summary="The pinned block, as a manifest file keeps it" >}}

This is the block the pin writes, as you would keep it in a file of [the apps directory]({{< relref "guides/serve-mcp-apps/gitops-apps-directory.md" >}}). The plural `repos`, the word the console uses for the tab and the questions, is the type plus s, so the block leaves it out.

```yaml
straza:
  objects:
    type: repo
    list:
      tool: repo_list
      attributes:
        - {name: team, setBy: org}
    tools:
      branch_list: {argument: repo}
      file_get: {argument: repo}
      issue_comment: {argument: repo}
      issue_create: {argument: repo}
      issue_get: {argument: repo}
      issue_list: {argument: repo}
      pr_comment: {argument: repo}
      pr_create: {argument: repo}
      pr_get: {argument: repo}
      pr_list: {argument: repo}
      pr_merge: {argument: repo}
      repo_get: {argument: repo}
      repo_list: all
      search_code: {argument: repo}
      search_issues: {argument: repo}
```


ghapp reports `setBy: org` for `team` when only the organization's owners may set the property, and `object` otherwise, including when it cannot read the property's settings. Without `GHAPP_MCP_ATTRIBUTES`, the block has no `attributes` line. The attribute shows as a column on the Repos tab and in the strazactl table. A role still covers repositories by name or covers every one, and the role editor's filter matches a repository's name and title only.
{{< /details >}}

## Give Straza the listing credential


Straza lists ghapp's repositories itself, in the background, so that a role can pick them by name. ghapp checks a token on that call as on every other, and the listing credential is that token. It is neither a GitHub token nor the server's own secret: ghapp keeps the GitHub App's key, and it accepts `GHAPP_MCP_LISTING_TOKEN` on `repo_list` alone. Any other tool called with it answers `The listing token opens repo_list only. Call this tool through Straza.` Straza sends it on its own calls to `repo_list` and on no agent's call, so a leaked listing token can only list names.

{{< console >}}

{{< clicks "MCP servers" "ghapp" "Overview" "Set it" >}}

After the pin, the card's second step reads `Give Straza the listing credential, a second token that ghapp accepts for repo_list only.` **Set it** opens the field on the row **Listing credential** of the Server authentication card, under the server's own secret. Until then the row reads `Not set.` with **Set one**, and its help icon says what the credential is for.

ghapp on this page already runs with its listing token, so paste that value into the box after `or paste` and press **Store**. The line under the field reads `Put the same value in ghapp's config, where it accepts the token for repo_list. Straza stores it sealed, never sends it on an agent's call, and never shows it again.` Once it is stored, the toast says `The listing credential for ghapp is stored. Straza is listing its repos now.`, the row reads `Stored, sealed` with **Set it again** and **Remove**, and with the value's fingerprint until you leave the page, and the card on Overview goes.

For a server whose config holds no value yet, press **Generate** instead. It fills the box with a random value, shown in clear with **Copy** and the line `Put this value in ghapp's config and restart ghapp, then press Store.` Press **Store** only after ghapp runs with the value as `GHAPP_MCP_LISTING_TOKEN`, because storing starts a listing that fails until ghapp holds it. If a listing failed anyway, **List again** on the Repos tab asks for another.
{{< /console >}}

{{< cli >}}

{{< command terminal="Your terminal" purpose="administration" >}}
```sh
strazactl apps secret set ghapp --listing
```
{{< /command >}}

```text
listing credential set for the MCP server ghapp, fingerprint 5606
```

Type the value of `GHAPP_MCP_LISTING_TOKEN` at the hidden prompt. The answer names the credential's fingerprint, never its value.
{{< /cli >}}


Straza lists ghapp's repositories within about 15 seconds of the store. A listing credential can be stored only once the type is pinned, which is why this step follows the publish. Until one is stored, the Repos tab says `ghapp has no listing credential, so Straza cannot list its repos. Set one under Server authentication on Overview, or with strazactl apps secret set ghapp --listing.`, and `strazactl apps objects ghapp` says `ghapp has no listing credential, so Straza cannot list its repos. Add one with strazactl apps secret set ghapp --listing, then refresh with strazactl apps objects refresh ghapp.`

## Check the list


{{< console >}}
{{< clicks "MCP servers" "ghapp" "Repos" >}}

The tab carries ghapp's plural and appears once the type is pinned. Its strip reads `ghapp lists its repos with repo_list.` and then the last listing with its count. Each repository is one row with the columns Repo, team, Covered by and In open drafts, and the help icon beside its name gives its id. Until a role covers it, a row reads `no role` and `No agent reaches it.` under Covered by. In open drafts names each open draft you may read that would add a role to the repository or drop one, such as `Draft 41 adds ghapp-write-payments`.
{{< /console >}}


{{< cli >}}
{{< command terminal="Your terminal" purpose="administration" >}}
```sh
strazactl apps objects ghapp
```
{{< /command >}}

```table
ghapp lists its repos with repo_list. Last listing less than a minute ago: 3 repos.
VALUE                      ID          TITLE    TEAM      COVERED BY
ghapp-straza-test/pay-api  1425832684  pay-api  payments  no role
ghapp-straza-test/pay-web  1425833185  pay-web  payments  no role
ghapp-straza-test/site     1425833478  site     web       no role
```

The first line says how the last listing went, with its count of repositories, and a repository reads `no role` until a role covers it.
{{< /cli >}}


In the demo organization, `ghapp-straza-test/pay-api` and `ghapp-straza-test/pay-web` carry the team `payments`, and `ghapp-straza-test/site` carries the team `web`. A value in the list is the exact owner/name a call must carry, byte for byte, so `ghapp-straza-test/Pay-API` is another name than `ghapp-straza-test/pay-api`. Straza lists again every 10 minutes. After you create a repository, `strazactl apps objects refresh ghapp` asks for a listing that one replica runs within 30 seconds, at most once a minute per server.

## Write the two roles


`ghapp-read` holds the read tools on every repository, the ones the App reaches later included. `ghapp-write-payments` holds the write tools on the two repositories of the team `payments`, by name, and a repository added later stays out until a person adds it. An agent that holds both reads everywhere and writes in those two repositories only.

{{< console >}}

{{< clicks "Roles" "New role" "Application role" >}}

On `Kind, server and name`, pick ghapp, type `write-payments` after its name and press **Next**. On Access, pick `Only the tools you tick` under **Which tools** and tick `issue_comment`, `issue_create`, `pr_comment`, `pr_create` and `pr_merge`. Under the tools, **On which repos** says `ghapp says 14 of its tools work on one repo. Pick which repos this role reaches with them.` It offers two cards:

| Card | Its line |
|---|---|
| Every repo | `All`, today's count, then `today, and every repo ghapp lists later.` |
| These repos | `Pick from ghapp's list. A repo added later stays out until you add it.` |

Pick **These repos** and tick `ghapp-straza-test/pay-api` and `ghapp-straza-test/pay-web` in the list, which `Filter repos` narrows. With **These repos** picked, `repo_list` cannot be ticked and reads `repo_list cannot be in a role on some repos: nothing keeps it to them. Make a role on every repo for it.` The fold `How it is stored` shows the access row as a role file holds it. Press **Next**, read Review, and press **Save and publish**.

Make `ghapp-read` the same way, with `branch_list`, `file_get`, `issue_get`, `issue_list`, `pr_get`, `pr_list`, `repo_get`, `search_code` and `search_issues`, and **Every repo**.


{{< clicks "Roles" "ghapp-write-payments" "Access" >}}

The column **On which repos** reads `2 repos: ghapp-straza-test/pay-api and ghapp-straza-test/pay-web`. On `ghapp-read` it reads `every repo`.
{{< /console >}}

{{< cli >}}

A role on a pinned server is written as a role file, because `strazactl roles create` takes no covers line. Both files go into one draft:

{{< command terminal="Your terminal" purpose="administration" >}}
```sh
mkdir -p ghapp-roles
cat > ghapp-roles/ghapp-read.yaml <<'EOF'
apiVersion: straza.dev/v1beta1
kind: Role
metadata:
    name: ghapp-read
spec:
    kind: application
    description: Read tools of ghapp on every repo
    server: ghapp
    bindings:
        - app: ghapp
          tools:
            - branch_list
            - file_get
            - issue_get
            - issue_list
            - pr_get
            - pr_list
            - repo_get
            - search_code
            - search_issues
          covers: every
EOF
cat > ghapp-roles/ghapp-write-payments.yaml <<'EOF'
apiVersion: straza.dev/v1beta1
kind: Role
metadata:
    name: ghapp-write-payments
spec:
    kind: application
    description: Write tools of ghapp on the payments repos
    server: ghapp
    bindings:
        - app: ghapp
          tools:
            - issue_comment
            - issue_create
            - pr_comment
            - pr_create
            - pr_merge
          covers:
            objects:
                - ghapp-straza-test/pay-api
                - ghapp-straza-test/pay-web
EOF
strazactl drafts check -f ghapp-roles/
strazactl drafts create -f ghapp-roles/ --note "Read on every repo, write on the payments repos"
```
{{< /command >}}

```text
  +   Role/ghapp-read
  +   Role/ghapp-write-payments
Checked against live state at 2026-10-19 17:37 UTC.
  info      Role/ghapp-read Nobody holds ghapp-read yet, so it reaches nothing until someone is assigned it.
  info      Role/ghapp-write-payments Nobody holds ghapp-write-payments yet, so it reaches nothing until someone is assigned it.
Nothing was stored. The documents can be published: strazactl drafts create -f ghapp-roles/
Created draft 40 with 2 documents. Nothing changes until a person publishes it.
  +   Role/ghapp-read
  +   Role/ghapp-write-payments
  info      Role/ghapp-read Nobody holds ghapp-read yet, so it reaches nothing until someone is assigned it.
  info      Role/ghapp-write-payments Nobody holds ghapp-write-payments yet, so it reaches nothing until someone is assigned it.
Publish draft 40 in the console under Drafts: http://localhost:8420/console/drafts/40
```


`drafts check` stores nothing, and `drafts create` stores the draft. A person publishes it in the console under **Drafts**, because a token never publishes.
{{< /cli >}}

{{< details summary="What the check refuses, and what a covered name stays bound to" >}}

The check refuses a role that nothing keeps to its repositories, and each refusal says what to change:

| The role file | The check says |
|---|---|
| has no `covers` line on its binding to ghapp | `ghapp-read binds tools that work on repos. Say which repos it covers: every repo, or these repos by name. In the role file, write covers: every, or covers: {objects: [<repo>, ...]}.` |
| names `ghapp-straza-test/Pay-API`, which differs in case | `ghapp-straza-test/Pay-API is not in ghapp's repo list. Compare the name with ghapp's Repos tab or strazactl apps objects ghapp. If the repo is new, refresh the list with strazactl apps objects refresh ghapp, then try again.` |
| puts `repo_list` in a role on some repos | `repo_list cannot be in a role on some repos: nothing keeps it to them. Make a role on every repo for it.` |
| writes `covers: {every: true}` | `Role ghapp-read does not read as a Role document: line 21: a covers mapping takes only the key objects, and this one carries every. Write covers: every for every object, or covers: {objects: [<name>, ...]} for some. Compare it with strazactl roles export ghapp-read and send the draft again.` |


Straza stores each named repository with its id. A role therefore keeps covering a repository that GitHub renames, and a repository deleted and made again under the old name is not covered by the old line. ghapp's own admin may write `covers: every` on a role ghapp owns while the setting `admin.serverAdminsCoverEvery` is true, its default. Set it to false to leave `every` to a global admin, and the **Every repo** card then reads `On this deployment only a global admin may make ghapp-read cover every repo, including the repos ghapp lists later. Name the repos it covers, or ask a global admin.` [Configuration]({{< relref "reference/configuration.md" >}}) lists the setting.
{{< /details >}}

{{< details summary="Let an agent draft the roles" >}}

An agent can write the role files of this page, and you publish them. A coding agent such as Claude Code with the Straza skill holds no Straza admin credential, so you run the strazactl reads it asks for and hand it their output. It proposes a draft through the tool `straza__draft_submit`, which the gateway lists only to a session that holds the role `straza-draft-config`, as [Propose and publish a change]({{< relref "guides/changes/propose-and-publish.md#let-an-agent-propose-a-change" >}}) shows. No agent can publish, and the listing credential never passes through one.

Tell the agent which repositories each team works in. It writes one role file per team from the list that `strazactl apps objects ghapp` prints, and `straza__draft_status` reads the server's check. A role can name a repository only once Straza has listed it, so the agent's draft comes after the pin and the listing credential. Run the strazactl reads in your own terminal, on a token that no agent can read, never in a terminal an agent shares.
{{< /details >}}

## Give the roles to an agent in midPoint


Your IGA tool sees what each role covers. Straza renders each role's SCIM group with `covers`, one value per access row and named repository. midPoint maps it to the role's extension item `strazaCovers` and shows it in the column Covers of its Straza application roles list. Read the values a certifier sees:

{{< command terminal="Terminal" purpose="SCIM, as midPoint reads it" >}}
```sh
for role in ghapp-read ghapp-write-payments; do
  curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -G http://localhost:8420/scim/v2/Groups \
    --data-urlencode "filter=displayName eq \"$role\"" \
    | jq -c '.Resources[0] | {displayName, covers: .["urn:straza:params:scim:schemas:extension:2.0:Group"].covers}'
done
```
{{< /command >}}

```text
{"displayName":"ghapp-read","covers":["ghapp:repo every"]}
{"displayName":"ghapp-write-payments","covers":["ghapp:repo \"ghapp-straza-test/pay-api\"","ghapp:repo \"ghapp-straza-test/pay-web\""]}
```

`ADMIN_API_TOKEN` holds a token with the scope `scim:read`, such as the one [midPoint]({{< relref "guides/connect-identity/midpoint.md#mint-the-token" >}}) mints. The quotes around each repository keep a repository named every from passing for every repository. Like the rest of the group's extension, `covers` is read-only, and a PATCH on it answers `400` with `scimType` `mutability`.


Assign the imported roles `AR:ghapp-read` and `AR:ghapp-write-payments` to the agent in midPoint, directly or through an access package, as [Assign an access package]({{< relref "guides/connect-identity/midpoint.md#assign-an-access-package" >}}) shows. midPoint writes one membership per role over SCIM. On the demo stack, this assigns both roles to `joe-java-developer-agent` over midPoint's REST interface:

{{< command terminal="Terminal" purpose="midPoint REST, as the midPoint admin" >}}
```sh
AGENT_OID=b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c4e
for role in AR:ghapp-read AR:ghapp-write-payments; do
  ROLE_OID=
  for try in $(seq 1 90); do
    ROLE_OID=$(curl -s -u 'administrator:StrazaEval5ecr3t!' -H 'Content-Type: application/xml' \
      -X POST --data-binary "<q:query xmlns:q=\"http://prism.evolveum.com/xml/ns/public/query-3\"><q:filter><q:equal><q:path>name</q:path><q:value>$role</q:value></q:equal></q:filter></q:query>" \
      http://localhost:8087/midpoint/ws/rest/roles/search | grep -o 'oid="[0-9a-f-]\{36\}"' | head -1 | cut -d'"' -f2)
    [ -n "$ROLE_OID" ] && break
    sleep 2
  done
  if [ -z "$ROLE_OID" ]; then
    echo "$role is not in midPoint after 180 seconds, so nothing was assigned. Check that midPoint's role LiveSync task runs, then run this again."
    break
  fi
  cat > assign.xml <<EOF
<objectModification xmlns="http://midpoint.evolveum.com/xml/ns/public/common/api-types-3"
    xmlns:c="http://midpoint.evolveum.com/xml/ns/public/common/common-3"
    xmlns:t="http://prism.evolveum.com/xml/ns/public/types-3">
  <itemDelta>
    <t:modificationType>add</t:modificationType>
    <t:path>c:assignment</t:path>
    <t:value><c:targetRef oid="$ROLE_OID" type="c:RoleType"/></t:value>
  </itemDelta>
</objectModification>
EOF
  curl -s -o /dev/null -w "$role HTTP %{http_code}\n" -u 'administrator:StrazaEval5ecr3t!' -H 'Content-Type: application/xml' \
    -X PATCH --data-binary @assign.xml http://localhost:8087/midpoint/ws/rest/users/$AGENT_OID
done
```
{{< /command >}}

```text
AR:ghapp-read HTTP 204
AR:ghapp-write-payments HTTP 204
```

`AGENT_OID` is the agent in your midPoint. midPoint imports a new Straza role at its next role LiveSync, which runs every 10 seconds on the demo stack, so the loop waits up to 180 seconds for each imported role to appear before it assigns it. The agent holds both roles from its next check-in.


The column Covers shows each role's `strazaCovers`, the same values SCIM gave above. Read them from midPoint's REST interface:

{{< command terminal="Terminal" purpose="midPoint REST, as the midPoint admin" >}}
```sh
for role in AR:ghapp-read AR:ghapp-write-payments; do
  curl -s -u 'administrator:StrazaEval5ecr3t!' -H 'Content-Type: application/xml' -H 'Accept: application/json' \
    -X POST --data-binary "<q:query xmlns:q=\"http://prism.evolveum.com/xml/ns/public/query-3\"><q:filter><q:equal><q:path>name</q:path><q:value>$role</q:value></q:equal></q:filter></q:query>" \
    http://localhost:8087/midpoint/ws/rest/roles/search \
    | jq -c '.object.object[0] | {name, strazaCovers: [.extension.strazaCovers] | flatten | map(.["@value"])}'
done
```
{{< /command >}}

```text
{"name":"AR:ghapp-read","strazaCovers":["ghapp:repo every"]}
{"name":"AR:ghapp-write-payments","strazaCovers":["ghapp:repo \"ghapp-straza-test/pay-api\"","ghapp:repo \"ghapp-straza-test/pay-web\""]}
```

## Read the refusal


Call a write tool on a repository the write role does not cover. From the agent's machine, through ghapp's own endpoint, this reads `ghapp-straza-test/pay-api` and then tries to open a pull request in `ghapp-straza-test/site`:

{{< command terminal="Agent's machine" purpose="client" >}}
```sh
{ printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"docs-example","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"repo_get","arguments":{"repo":"ghapp-straza-test/pay-api"}}}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"pr_create","arguments":{"repo":"ghapp-straza-test/site","title":"Bump the Go toolchain","head":"bump-go","base":"main"}}}'; sleep 5; } \
  | straza mcp ghapp
```
{{< /command >}}

```text
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"extensions":{"io.modelcontextprotocol/ui":{}},"logging":{},"resources":{"listChanged":true},"tools":{"listChanged":true}},"protocolVersion":"2025-06-18","serverInfo":{"name":"straza","version":"v1.1.0-138-g8cd72d69"}}}
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"Straza: pr_create on ghapp-straza-test/site was refused: none of your roles on ghapp covers that repo. Your roles cover pr_create on ghapp-straza-test/pay-api and ghapp-straza-test/pay-web. Check the repo's name, or ask an admin for a role that covers it."}],"isError":true}}
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ghapp-straza-test/pay-api (id 1425832684): private, default branch \"main\"."}],"structuredContent":{"archived":false,"defaultBranch":"main","fullName":"ghapp-straza-test/pay-api","htmlUrl":"https://github.com/ghapp-straza-test/pay-api","id":"1425832684","name":"pay-api","owner":"ghapp-straza-test","ownerType":"Organization","private":true}}}
```

The first call goes through, because `ghapp-read` covers every repository. The second never reaches ghapp, which is why its refusal comes back before the answer to `repo_get`. Straza answers it with a tool result marked as an error, and the agent reads `Straza: pr_create on ghapp-straza-test/site was refused: none of your roles on ghapp covers that repo. Your roles cover pr_create on ghapp-straza-test/pay-api and ghapp-straza-test/pay-web. Check the repo's name, or ask an admin for a role that covers it.`


That sentence names only repositories the agent's own roles cover, at most ten. A repository that no held role covers and one the list does not hold get the same answer, so an agent learns nothing about repositories it cannot use. The check runs before any policy, so no policy rule can allow a call it refuses, and the refusal spends no approval.

{{< console >}}

{{< clicks "Audit" "deny" >}}

The newest row reads `ghapp.pr_create on ghapp-straza-test/site` under **Action**, followed by the call's arguments, with the agent's sentence under **Reason**. Select it. The sheet has a row named Repo with the value and its id in ghapp's list.
{{< /console >}}

{{< cli >}}

{{< command terminal="Your terminal" purpose="administration" >}}
```sh
strazactl audit tail --app ghapp --type straza.audit.mcp --limit 3
```
{{< /command >}}

```text
#382 [joe-java-developer-agent] {"data":{"app":"ghapp","count":14,"default":false,"effect":"allow","event":"tools.list","granted":false,"harness":"claude-code/mcp","reason":"catalog served","ruleId":"","session":"01a156a5-c19a-7b1f-af1e-a4df00283dde","setName":"","snapshot":"de711b9f89fd7ef39105fa6f30103ce06449f1969206f97ffa8b71564c5eace1","tool":"","toolName":"","user":"01a1547c-366e-7767-97eb-caa598255d94"},"id":"ea5ff55c-a242-4fbc-be4f-e7c477802997","source":"strazad","specversion":"1.0","time":"2026-10-20T00:11:06.076296916Z","type":"straza.audit.mcp"}
#383 [joe-java-developer-agent] {"data":{"app":"ghapp","arguments":"{\"repo\":\"ghapp-straza-test/pay-api\"}","bindingId":"01a1553d-bc97-7c68-a755-4d26e5d08b2f","credentialId":"01a1553c-ae88-7e94-9afc-dc6cfc5d14be","credentialSource":"shared","default":true,"effect":"allow","event":"tool.pre","granted":true,"harness":"claude-code/mcp","object":{"id":"1425832684","reach":"argument","type":"repo","value":"ghapp-straza-test/pay-api"},"reason":"allowed by role access. No policy rule gates this tool.","role":"ghapp-read","ruleId":"","session":"01a156a5-c19a-7b1f-af1e-a4df00283dde","setName":"","snapshot":"de711b9f89fd7ef39105fa6f30103ce06449f1969206f97ffa8b71564c5eace1","tool":"mcp.call","toolName":"repo_get","user":"01a1547c-366e-7767-97eb-caa598255d94"},"id":"7443f5cc-7af8-493b-b12b-8a7a50c302a9","source":"strazad","specversion":"1.0","time":"2026-10-20T00:11:06.086979293Z","type":"straza.audit.mcp"}
#384 [joe-java-developer-agent] {"data":{"app":"ghapp","arguments":"{\"repo\":\"ghapp-straza-test/site\",\"title\":\"Bump the Go toolchain\",\"head\":\"bump-go\",\"base\":\"main\"}","default":false,"effect":"deny","event":"tool.pre","granted":false,"harness":"claude-code/mcp","object":{"id":"1425833478","reach":"argument","type":"repo","value":"ghapp-straza-test/site"},"reason":"Straza: pr_create on ghapp-straza-test/site was refused: none of your roles on ghapp covers that repo. Your roles cover pr_create on ghapp-straza-test/pay-api and ghapp-straza-test/pay-web. Check the repo's name, or ask an admin for a role that covers it.","rolesChecked":["ghapp-write-payments"],"ruleId":"","session":"01a156a5-c19a-7b1f-af1e-a4df00283dde","setName":"","snapshot":"de711b9f89fd7ef39105fa6f30103ce06449f1969206f97ffa8b71564c5eace1","tool":"mcp.call","toolName":"pr_create","user":"01a1547c-366e-7767-97eb-caa598255d94"},"id":"fdadeb1e-3f0b-42f6-8159-a1392a6bf3c3","source":"strazad","specversion":"1.0","time":"2026-10-20T00:11:06.093335815Z","type":"straza.audit.mcp"}
```

The deny record carries `object`, with the type `repo`, the reach `argument`, the value `ghapp-straza-test/site` and its id in ghapp's list, `1425833478`. It also carries `rolesChecked` with `ghapp-write-payments`, the held role that binds `pr_create`. It has no `role` and no `bindingId`, because no role admitted the call.
{{< /cli >}}

{{< details summary="New repositories, and approvals on a covered call" >}}

Roles on every repository cover one the list does not hold yet, so a repository created a minute ago works at once through such a role. When a refused call names a repository the list does not hold, Straza asks for a listing, at most once a minute, and the new repository then shows in the list, where a person can add it to a role that names repositories.


A covered call then meets your policies like any other call. An approval that a policy asks for on such a call holds for that repository alone, because Straza ties the approval to the repository's type and value. The approval card names the repository only inside the call's arguments, so with `approval.preview.enabled` set to false the approver does not see it. [Events]({{< relref "reference/events.md" >}}) lists every field of the record.
{{< /details >}}


## What ghapp promises {.nostep}


Straza checks the repository a call names, and ghapp keeps the call to that repository. Every marked server makes three promises that Straza cannot check: a call touches only the object its argument names, the value names exactly one object at the time of the call, and the server accepts calls only from the gateway. Straza checks repositories in its gateway only, and its harness hooks never run this check, so an agent that reached ghapp some other way would meet no repository check. [Object marks for server authors]({{< relref "reference/object-marks.md#promises" >}}) states the promises for a server's author, and [the security model]({{< relref "security/security-model.md#the-object-boundary" >}}) draws the boundary.

{{< details summary="How ghapp keeps them" >}}
ghapp compares the name with GitHub's own spelling byte for byte and never follows a rename redirect. When a role on some repositories admitted the call, Straza sends the repository's id from its list as `params._meta["ai.straza/object-id"]`, and ghapp refuses the call when the name now leads to another id, such as a repository deleted and made again. It then makes a GitHub token for that one repository with only the tool's permissions. Its last guard is the caller: ghapp accepts the gateway secret, a token from a listed identity provider client whose only credential is Straza's client assertion key, and the listing token on `repo_list` alone.
{{< /details >}}

## Next {.nostep}

- [Objects]({{< relref "concepts/objects.md" >}}) explains the model behind these steps.
- [Object marks for server authors]({{< relref "reference/object-marks.md" >}}) is the contract for a server that wants the same limits.
- [Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) previews which tools a role's sessions see and puts a tool behind approval.
- [When the server's tools change]({{< relref "guides/serve-mcp-apps/add-a-server.md#tool-changes" >}}) says what Straza does when ghapp adds, removes or changes a tool.
- [midPoint]({{< relref "guides/connect-identity/midpoint.md" >}}) hands the roles out through access packages and takes them away again.
