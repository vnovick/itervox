#!/bin/sh
# Example itervox custom backend (itervox agent protocol, version 1).
#
# Wraps any command-line tool that reads a prompt on stdin and prints plain
# text, and turns it into an itervox agent:
#
#   agent:
#     backend: custom
#     command: ./docs/examples/custom-agent.sh
#
# The tool to run is $CUSTOM_AGENT_TOOL (a command line, run by sh); it gets
# the prompt on stdin and each line it prints becomes an assistant text
# event. Its exit status decides the turn: 0 is "done", anything else
# "failed". Replace the tool call below with your own agent loop.
#
# What itervox gives the wrapper (see the Backends guide):
#   stdin                    the whole prompt, then EOF
#   ITERVOX_AGENT_PROTOCOL   1
#   ITERVOX_PERMISSION_MODE  bypass | sandbox
#   ITERVOX_SESSION_ID       set on a continuation turn: the id this wrapper
#                            reported earlier
# The working directory is the issue's workspace.

set -u

tool=${CUSTOM_AGENT_TOOL:-cat}

# json_string prints its argument as a JSON string: backslash, double quote
# and tab are escaped, other control characters dropped.
json_string() {
	printf '%s' "$1" | tr -d '\000-\010\013-\037' |
		sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/	/\\t/g' |
		awk 'BEGIN { printf "\"" } NR > 1 { printf "\\n" } { printf "%s", $0 } END { printf "\"" }'
}

# Resume: keep the session id itervox passes back, else start a new one.
session=${ITERVOX_SESSION_ID:-"custom-$$-$(date +%s)"}
printf '{"type":"session","session_id":%s}\n' "$(json_string "$session")"

# Read the prompt before starting the tool, so the tool's own stdin is the
# prompt file and nothing else.
prompt_file=$(mktemp "${TMPDIR:-/tmp}/itervox-prompt.XXXXXX") || exit 1
trap 'rm -f "$prompt_file" "$prompt_file.out"' EXIT
cat > "$prompt_file"

printf '{"type":"tool_call","name":"bash","input":{"command":%s}}\n' "$(json_string "$tool")"
sh -c "$tool" < "$prompt_file" > "$prompt_file.out" 2>&1
status=$?
printf '{"type":"tool_result","name":"bash","input":{"command":%s},"is_error":%s}\n' \
	"$(json_string "$tool")" "$([ "$status" -eq 0 ] && echo false || echo true)"

while IFS= read -r line || [ -n "$line" ]; do
	[ -n "$line" ] && printf '{"type":"text","text":%s}\n' "$(json_string "$line")"
done < "$prompt_file.out"

if [ "$status" -ne 0 ]; then
	printf '{"type":"failed","reason":%s}\n' "$(json_string "$tool exited $status")"
	exit 1
fi
printf '{"type":"done","result":"finished"}\n'
