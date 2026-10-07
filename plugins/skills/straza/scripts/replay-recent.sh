#!/bin/sh
# replay-recent.sh: replay a user's recent tool decisions through a local
# policy file and show where the file would decide differently from the
# policies live right now.
#
# Usage: replay-recent.sh <username> <policy.yaml> [limit]
# Needs strazactl logged in as an administrator and jq on PATH. It runs on
# the person's login, inside a coding agent too, because audit tail reads
# and policy simulate is a check that changes nothing. Nothing is recorded
# and nothing is published.
set -eu

usage='usage: replay-recent.sh <username> <policy.yaml> [limit]'
user=${1:?$usage}
policy_file=${2:?$usage}
limit=${3:-50}

command -v jq >/dev/null 2>&1 || { echo "replay-recent.sh: jq is required" >&2; exit 1; }
[ -f "$policy_file" ] || { echo "replay-recent.sh: the local policy file $policy_file was not found" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

strazactl audit tail --user "$user" --limit "$limit" > "$tmp/records"

n=0
differ=0
while IFS= read -r line; do
  case $line in '#'*) ;; *) continue ;; esac
  seq=${line%% *}
  json=$(printf '%s' "$line" | sed 's/^#[0-9]* \[[^]]*\] //')
  type=$(printf '%s' "$json" | jq -r '.type')
  case $type in straza.audit.tool|straza.audit.mcp) ;; *) continue ;; esac
  # A served tools/list, and a list or read of views, is a gateway record
  # too, but no decision to replay.
  case $(printf '%s' "$json" | jq -r '.data.event') in tools.list|resources.list|resources.read) continue ;; esac

  # The event the record was judged on, in the shape the simulator reads.
  printf '%s' "$json" | jq -c '
    {kind: .data.event, tool: .data.tool}
    + (if (.data.command // "") != "" then {command: .data.command} else {} end)
    + (if (.data.app // "") != "" then {app: .data.app, toolName: .data.toolName} else {} end)
    + (if (.data.paths // null) != null then {paths: .data.paths} else {} end)
    + (if (.data.workspace // "") != "" then {workspace: .data.workspace} else {} end)
  ' > "$tmp/event.json"

  what=$(printf '%s' "$json" | jq -r 'if (.data.command // "") != "" then .data.command elif (.data.app // "") != "" then .data.app + "__" + (.data.toolName // "") else (.data.tool // "") + " " + ((.data.paths // []) | join(" ")) end')
  was=$(printf '%s' "$json" | jq -r '.data.effect')
  # simulate exits 0 on every verdict, so a failure here is a real error.
  if ! out=$(strazactl policy simulate --user "$user" --event-json "$tmp/event.json" -f "$policy_file" 2>&1); then
    printf 'replay-recent.sh: strazactl policy simulate failed for record %s:\n%s\n' "$seq" "$out" >&2
    exit 1
  fi
  verdict=$(printf '%s\n' "$out" | grep -E '^(live and file agree|the live policy says)' || true)
  if [ -z "$verdict" ]; then
    printf 'replay-recent.sh: record %s got no verdict line, so the output of strazactl policy simulate has changed and this script needs an update. Nothing was counted. The output was:\n%s\n' "$seq" "$out" >&2
    exit 1
  fi
  case $verdict in 'the live policy says'*) differ=$((differ + 1)) ;; esac

  printf '%s  was %-5s  %s  <- %s\n' "$seq" "$was" "$verdict" "$what"
  n=$((n + 1))
done < "$tmp/records"

echo "$n record(s) of $user replayed against $policy_file; the file changes $differ of them"
