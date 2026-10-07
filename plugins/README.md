# Straza skills for AI coding agents

Skills that teach Claude Code, Gemini CLI and Codex how to operate [Straza](https://straza.ai), the control plane between your identity manager and your AI agents. This directory is the plugin root inside the Straza repository: `.claude-plugin/` for Claude Code, `gemini-extension.json` for Gemini CLI and `.codex-plugin/` for Codex, all reading the one skill under `skills/`. The docs site serves the same skill from https://docs.straza.ai/.well-known/skills/ with a marketplace file beside it.

## The skill

One skill, `straza`, whose body is a map and whose reference files carry the jobs:

| The job | Reference |
|---|---|
| Put one agent under policy in five minutes, ask before a command runs, pull the kill switch | `references/quickstart.md` |
| Write a policy that blocks, holds, tickets, records or classifies, with the validator and the simulator in the loop | `references/policy.md` and `references/examples/` |
| Replay a role's recent real decisions through a local policy file before it is published | `scripts/replay-recent.sh` |
| Explain a deny from the audit record to the rule to the fix | `references/policy.md` |
| Add an MCP server end to end: manifest, credential, application role, approvals, phone, assignment | `references/servers.md` |
| Map identity-manager roles to Straza roles and provision agents with sponsors | `references/identity.md` and `references/identity/` |
| Build audit evidence, SIEM queries and a control mapping from the audit chain | `references/evidence.md` |

Every command is quoted from the public docs at https://docs.straza.ai/ and the skill defers to the docs when they disagree.

## Install

Claude Code, from inside a session. The marketplace file lives on the docs domain and points at this directory in the repository:

```text
/plugin marketplace add https://docs.straza.ai/.well-known/claude-code/marketplace.json
/plugin install straza@straza
```

Adding the repository itself works too, with `/plugin marketplace add strazahq/straza`.

Codex, Cursor and every harness the Skills CLI supports, from the docs domain:

```sh
npx skills add https://docs.straza.ai
```

Gemini CLI, from the repository path:

```sh
gemini skills install https://github.com/strazahq/straza.git --path plugins/skills/straza
```

Updates follow the version in the manifests. Claude Code users update with `/plugin marketplace update straza` and `/plugin update straza`, or turn on auto-update for the marketplace. Skills CLI users run `npx skills update -y`.

## What this bundle does not do

It installs no hooks and carries no credentials. Governance comes from `straza install`, which wires the harness hooks and the gateway on a machine, and from the managed install an administrator runs on a shared one. These skills help people and agents use Straza. They are never the enforcement point.

## License

Apache-2.0. See `LICENSE`.
