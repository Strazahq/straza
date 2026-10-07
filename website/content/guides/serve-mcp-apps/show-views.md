---
title: Show MCP Apps views
description: Let one MCP server show its interactive views in a chat app, with every click decided, held or refused like any other tool call.
pagetype: how-to
weight: 35
draft: false
tested:
  version: v1.1.0-32-g2d89c4db
  platform: Linux, against the enterprise demo stack and its views-demo server, with the gateway called over curl and through straza mcp views-demo on stdin as the AI agent joe, and alice approving and denying the holds over the admin API. The Claude Desktop steps were not run on this walk. On 2026-10-01 Claude Desktop chat on macOS rendered the midpoint server's approval inbox through straza mcp midpoint on the same stack
  date: 2026-10-01
applies_to: both
keywords: mcp apps views ui resources claude desktop
who: You, as a global admin of MCP servers
where: A terminal with strazactl, and a chat app on a Mac
steps: true
---


MCP Apps is an extension of the Model Context Protocol. A server links a tool to a view, an HTML page at a `ui://` address, and a chat app that supports the extension shows that page beside the tool's result in a sandboxed frame. Every button in the view makes an ordinary tool call. Through Straza, that call takes the path an agent's call takes: the access row, the policy, a hold for a person's approval, the credential and the audit record. So a view gives nobody a tool they could not already call.

## Turn views on for one server


Views are off for every server until an administrator turns them on in that server's manifest. The switch sits in the exposure block, beside the tools list it needs:

```yaml
straza:
  exposure:
    tools: ["*"]
    views: true
```


No console sheet changes this switch, so publish it like any other manifest change. Change the file you keep, such as one in [the apps directory]({{< relref "guides/serve-mcp-apps/gitops-apps-directory.md" >}}), and publish the draft it proposes. Without a file of your own, start from the live manifest:

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl apps export views-demo > views-demo.yaml
strazactl drafts create -f views-demo.yaml
strazactl drafts publish <id>
```
{{< /command >}}

Edit `views-demo.yaml` between the first two commands, and write back every value the export masks first, as [the apps directory]({{< relref "guides/serve-mcp-apps/gitops-apps-directory.md#the-directory-and-the-other-ways-in" >}}) explains. `strazactl apps install -f views-demo.yaml` publishes the same change in one step. Only the scope `apps:write` or the role `straza-global-mcp-admin` may change the switch, because with views on, chat apps show the server's own HTML to the people who use it. A server's own admin role is refused with "changing straza.exposure.views of a server needs the scope apps:write or the role straza-global-mcp-admin, because with views on, chat apps show the server's own HTML pages to the people who use it. Ask a holder of straza-global-mcp-admin to make that change."


With the switch off, nothing changes for the server: Straza's handshake with it stays the same, and no tool carries a view link. With it on, Straza tells the server that chat apps can show views and reads each view a listed tool links. It serves a view only when the view is one HTML text of at most 2 MiB, and at most 32 views of one server. Straza reads a view again within 10 minutes, or at once after `strazactl apps recheck views-demo`. A changed view raises the same drift alarm as a changed tool, and a view the server stops answering for stays served for at most 20 minutes after its last answer.

The demo stack ships two servers with views on. `views-demo`, the official MCP Apps example server, has one tool, `get-time`, which opens a clock card whose button asks for the time again. `midpoint` has four views: the approval inbox opens from `list_work_items` and `decide_work_item`, Get access from `list_requestable_roles` and `request_role`, My requests from `list_my_requests`, `get_case` and `cancel_request`, and My team's access from `list_my_team`, `get_user_assignments` and `unassign_role`.

## Connect a chat app to the server's own endpoint


Straza's combined endpoint, `/mcp`, names every tool after its server, as `views-demo__get-time`, while a view calls its tools by the server's own names. Views are therefore served only on the server's own endpoint, `/mcp/views-demo`. That endpoint lists the server's tools under their own names with their view links, applies the same rules, holds and records as `/mcp`, and lists none of the built-in straza tools.


`straza mcp views-demo` connects a chat app to that endpoint over stdio, on a machine where `straza enroll` has run. For Claude Desktop on macOS, add the entry to `~/Library/Application Support/Claude/claude_desktop_config.json`, with the path of your own `straza` binary, then restart Claude Desktop:

```json
{
  "mcpServers": {
    "views-demo": {
      "command": "/usr/local/bin/straza",
      "args": ["mcp", "views-demo"]
    }
  }
}
```

Ask Claude for the time on the server. The `get-time` tool runs and the clock card renders, and its Get Server Time button calls `get-time` again through Straza. Claude Desktop on Windows reportedly shows no view for a local stdio server (issue 1069 in anthropics/claude-ai-mcp), so walk this on macOS. Register a server's own entry only in chat apps that show views. In Claude Code, the hooks take only the `straza` entry's tools for gateway calls, so keep `straza mcp` there.


The endpoint advertises the extension in its handshake, and `tools/list` keeps the view link. The walk's answers, trimmed:

```text
{"extensions":{"io.modelcontextprotocol/ui":{}},"resources":{},"tools":{"listChanged":true}}
{"name":"get-time","meta":{"ui":{"resourceUri":"ui://get-time/mcp-app.html"},"ui/resourceUri":"ui://get-time/mcp-app.html"}}
{"uri":"ui://get-time/mcp-app.html","mimeType":"text/html;profile=mcp-app","bytes":217931}
```

A misspelled or ungranted server, or a stopped one, gets one sentence, so the endpoint reveals no server names: "Straza: no MCP server named "midpoint-prod" is in your catalog. Check the name, or ask an administrator for access to it."


Straza lists and serves a view exactly when the caller can use a tool that opens it, after the session's policy hides the tools it denies. No new access row exists for views. Anyone else asking for the address gets "Straza: the view "ui://get-time/other.html" is not available to you on this server. A view is shown only to people who can use a tool that opens it."

## A click that waits or is refused {.nostep}


When a policy holds a click for a person's approval, the gateway keeps the call open for up to two minutes, then answers. On a server whose views are on, the answer carries the decision field the view contract defines, `_meta["intermediary/decision"]`, with a sentence for the person. The walk's held answer:

```json
{"v":1,"decision":"held","audited":true,"reason":"This action waits for a person's approval. Once it is approved, do it again.","ref":"01a0f68f-0ad0-7efe-992c-08177822dedd","expiresAt":"2026-10-01T08:32:44Z","source":"Straza"}
```

A Straza hold never runs the call later on its own. Once the approver says yes, the person clicks again, and that call runs once. A refused call carries `"decision":"denied"` with the reason, for example "Approval denied by alice (ref 01a0f691-2bd3-75f8-8de4-aa70d77bf30a)". Straza removes the same field from a server's own answers on this endpoint, so a server cannot pass a call that ran as a hold or a refusal.

## What Straza records {.nostep}


Every list and read of views writes one `straza.audit.mcp` record, with the event `resources.list` or `resources.read`, the server in `app`, and the address in `uri`, cut at 8 KiB. Clicks are tool calls, so each one writes the record a tool call writes. A `tools/list` on the server's endpoint names the server in `app`. The walk's records, trimmed:

```text
{"event":"resources.list","app":"views-demo","effect":"allow","reason":"views served","count":1}
{"event":"resources.read","app":"views-demo","uri":"ui://get-time/mcp-app.html","effect":"allow","reason":"view served"}
{"event":"resources.read","app":"views-demo","uri":"ui://get-time/other.html","effect":"deny"}
```

[Events and the audit record]({{< relref "reference/events.md" >}}) lists every field.
