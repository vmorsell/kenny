#!/bin/sh
# kenny-cli.sh — talk to a running Kenny instance from the command line
#
# Usage:
#   ./kenny-cli.sh <host> <command> [args]
#
# Commands:
#   status              — current life info
#   msg <text>          — queue a message for next life
#   msgs                — list pending messages
#   journal [N [kind]]  — last N journal entries (default 20); optionally filter by kind
#   lives [N]           — per-life outcome summaries (default 10)
#   commits [N]         — recent git commits (default 10)
#   note                — show pinned note
#   note set <text>     — set pinned note
#   note clear          — clear pinned note
#   inflight            — open inflight tasks
#
# Examples:
#   ./kenny-cli.sh localhost:8080 status
#   ./kenny-cli.sh localhost:8080 msg "write a python script that sorts a CSV"
#   ./kenny-cli.sh localhost:8080 journal 5 message_response
#   ./kenny-cli.sh localhost:8080 note set "priority: fix the auth bug"

set -e

HOST="${1:?usage: $0 <host> <command> [args]}"
CMD="${2:?usage: $0 <host> <command> [args]}"
shift 2

BASE="http://${HOST}"

_get() { curl -sf "${BASE}${1}"; }
_post() { curl -sf -X POST -H "Content-Type: application/json" -d "${2}" "${BASE}${1}"; }
_delete() { curl -sf -X DELETE "${BASE}${1}"; }
_json() { command -v jq >/dev/null 2>&1 && jq -r "${1}" || cat; }

case "$CMD" in
  status)
    _get /api/status | _json '
      "life:     \(.life_id)\n" +
      "boot:     \(.boot_at)\n" +
      "death:    \(.expected_death_at)\n" +
      "remaining: \(.remaining_seconds)s\n" +
      "inflight: \(.inflight_count)"
    '
    ;;

  msg)
    TEXT="${*:?msg requires text}"
    PAYLOAD="$(printf '{"content":"%s"}' "$(printf '%s' "$TEXT" | sed 's/"/\\"/g')")"
    _post /api/message "$PAYLOAD" | _json '"queued: \(.content) at \(.received_at)"'
    ;;

  msgs)
    _get /api/messages | _json '.[] | "[\(.received_at)] \(.content)"'
    ;;

  journal)
    N="${1:-20}"
    KIND="${2:-}"
    URL="/api/journal?limit=${N}"
    [ -n "$KIND" ] && URL="${URL}&kind=${KIND}"
    _get "$URL" | _json '.[] | "[life \(.life_id) | \(.at) | \(.kind)] \(.message[0:120])"'
    ;;

  lives)
    N="${1:-10}"
    _get "/api/lives?n=${N}" | _json '.[] | "[life \(.life_id) | \(.at) | \(.kind)] \(.summary)"'
    ;;

  commits)
    N="${1:-10}"
    _get "/api/commits?n=${N}" | _json '.[] | "\(.sha[0:7]) \(.subject)"'
    ;;

  note)
    SUBCMD="${1:-}"
    case "$SUBCMD" in
      "")
        _get /api/note | _json 'if .set then "note: \(.content)" else "(no note set)" end'
        ;;
      set)
        shift
        TEXT="${*:?note set requires text}"
        PAYLOAD="$(printf '{"content":"%s"}' "$(printf '%s' "$TEXT" | sed 's/"/\\"/g')")"
        _post /api/note "$PAYLOAD" && echo "note set"
        ;;
      clear)
        _delete /api/note && echo "note cleared"
        ;;
      *)
        echo "unknown note subcommand: $SUBCMD" >&2
        exit 1
        ;;
    esac
    ;;

  inflight)
    _get /api/inflight | _json '.[] | "[id=\(.id) | life \(.life_id) | \(.kind)] \(.payload)"'
    ;;

  *)
    echo "unknown command: $CMD" >&2
    echo "commands: status msg msgs journal lives commits note inflight" >&2
    exit 1
    ;;
esac
