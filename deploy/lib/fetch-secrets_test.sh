#!/usr/bin/env bash
#
# CORE-061 test for deploy/lib/fetch-secrets.sh. Run: make deploy-test
# (or bash deploy/lib/fetch-secrets_test.sh). Needs bash and python3.
#
# gcloud, aws and az are replaced by stubs on PATH that serve fixture secrets
# (and, like a chatty CLI, also echo the value to their own stderr, which the
# script must swallow). The test proves, for every provider:
#   1. values with spaces, '=', '#', both quote kinds, backslashes, '$', '`'
#      and embedded newlines round-trip through the EnvironmentFile quoting,
#      read back with a line-for-line port of systemd's parser
#      (src/basic/env-file.c, parse_env_file_internal; see parse_envfile below);
#   2. the file mode is 0600;
#   3. a re-run is idempotent (byte-identical file) and a failing re-run leaves
#      the previous file untouched;
#   4. no secret appears on the script's stdout or stderr.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/fetch-secrets.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

# ── fixtures ────────────────────────────────────────────────────────────────
FIX="$WORK/secrets"
mkdir -p "$FIX" "$WORK/bin"
# name|value — every value carries SENTINEL so leak checks can grep for it.
put() { printf '%s' "$2" > "$FIX/$1"; }
put s-spaces  '  SENTINEL-spaces  with   runs and trailing  '
put s-equals  'SENTINEL=a=b==c='
put s-hash    '#SENTINEL # not a comment ;also not'
put s-quotes  "SENTINEL \"double\" and 'single' and \"'mixed'\""
# shellcheck disable=SC1003 # a literal trailing backslash is the point
put s-bslash  'SENTINEL\back\\slash\n\t\"\'
# shellcheck disable=SC2016 # literal $ and backticks are the point
put s-dollar  'SENTINEL $HOME ${PATH} $(id) `uname` \$'
put s-newline "$(printf 'SENTINEL line1\nline2 = x\n#line3\n  indented\n\nafter-blank')"
put s-plain   'SENTINELplaintoken0123456789'
MAP="K_SPACES=s-spaces K_EQUALS=s-equals K_HASH=s-hash K_QUOTES=s-quotes K_BSLASH=s-bslash K_DOLLAR=s-dollar K_NEWLINE=s-newline K_PLAIN=s-plain"

stub() { # stub NAME: prints the secret named by --secret / --secret-id / --name
  cat > "$WORK/bin/$1" <<'EOF'
#!/usr/bin/env bash
name=""
for a in "$@"; do
  case "$a" in
    --secret=*|--secret-id=*|--name=*) name="${a#*=}" ;;
  esac
done
prev=""
for a in "$@"; do
  case "$prev" in --secret|--secret-id|--name) name="$a" ;; esac
  prev="$a"
done
f="$STUB_FIXTURES/$name"
[[ -f "$f" ]] || { echo "stub: no such secret $name" >&2; exit 1; }
cat "$f" >&2            # a chatty CLI: fetch-secrets.sh must discard this
cat "$f"; printf '\n'   # real CLIs end their output with a newline
EOF
  chmod +x "$WORK/bin/$1"
}
stub gcloud; stub aws; stub az
export PATH="$WORK/bin:$PATH" STUB_FIXTURES="$FIX"

# ── systemd EnvironmentFile parser (port of env-file.c) ─────────────────────
# Writes each parsed KEY's value to $2/KEY, byte-exact.
parse_envfile() {
  python3 - "$1" "$2" <<'PY'
import os, sys
WHITESPACE, NEWLINE, COMMENTS, NEED_ESC = " \t\n\r", "\n\r", "#;", '"\\`$'
data = open(sys.argv[1], encoding="utf-8").read()
out = sys.argv[2]
state, key, value, last_ws = "PRE_KEY", "", "", None
pairs = []
def push():
    global key, value
    k = key.rstrip(WHITESPACE)
    if k:
        pairs.append((k, value))
    key, value = "", ""
for c in data:
    if state == "PRE_KEY":
        if c in COMMENTS: state = "COMMENT"
        elif c not in WHITESPACE: state, key = "KEY", c
    elif state == "KEY":
        if c in NEWLINE: state, key = "PRE_KEY", ""
        elif c == "=": state, last_ws = "PRE_VALUE", None
        else: key += c
    elif state == "PRE_VALUE":
        if c in NEWLINE: state = "PRE_KEY"; push()
        elif c == "'": state = "SINGLE_QUOTE_VALUE"
        elif c == '"': state = "DOUBLE_QUOTE_VALUE"
        elif c == "\\": state = "VALUE_ESCAPE"
        elif c not in WHITESPACE: state = "VALUE"; value += c
    elif state == "VALUE":
        if c in NEWLINE:
            state = "PRE_KEY"
            if last_ws is not None: value = value[:last_ws]
            push(); last_ws = None
        elif c == "\\": state = "VALUE_ESCAPE"; last_ws = None
        else:
            if c in WHITESPACE:
                if last_ws is None: last_ws = len(value)
            else: last_ws = None
            value += c
    elif state == "VALUE_ESCAPE":
        state = "VALUE"
        if c not in NEWLINE: value += c
    elif state == "SINGLE_QUOTE_VALUE":
        if c == "'": state = "PRE_VALUE"
        else: value += c
    elif state == "DOUBLE_QUOTE_VALUE":
        if c == '"': state = "PRE_VALUE"
        elif c == "\\": state = "DOUBLE_QUOTE_VALUE_ESCAPE"
        else: value += c
    elif state == "DOUBLE_QUOTE_VALUE_ESCAPE":
        state = "DOUBLE_QUOTE_VALUE"
        if c in NEED_ESC: value += c
        elif c == "\n": pass            # line continuation
        else: value += "\\" + c
    elif state == "COMMENT":
        if c == "\\": state = "COMMENT_ESCAPE"
        elif c in NEWLINE: state = "PRE_KEY"
    elif state == "COMMENT_ESCAPE":
        state = "COMMENT"
if state in ("PRE_VALUE", "VALUE", "VALUE_ESCAPE", "SINGLE_QUOTE_VALUE",
             "DOUBLE_QUOTE_VALUE", "DOUBLE_QUOTE_VALUE_ESCAPE"):
    if state == "VALUE" and last_ws is not None: value = value[:last_ws]
    push()
os.makedirs(out, exist_ok=True)
for k, v in pairs:
    with open(os.path.join(out, k), "w", encoding="utf-8", newline="") as f:
        f.write(v)
PY
}

file_mode() { stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"; }

# ── parser self-check against systemd's documented examples ─────────────────
cat > "$WORK/doc.env" <<'EOF'
# comment
A=plain value
B="quoted \"x\" \\ \$y"
C='single "kept" \n'
D="multi
line"
EOF
parse_envfile "$WORK/doc.env" "$WORK/doc.out"
# shellcheck disable=SC2016 # literal $y expected
if [[ "$(cat "$WORK/doc.out/A")" == 'plain value' \
   && "$(cat "$WORK/doc.out/B")" == 'quoted "x" \ $y' \
   && "$(cat "$WORK/doc.out/C")" == 'single "kept" \n' \
   && "$(cat "$WORK/doc.out/D")" == $'multi\nline' ]]; then
  pass "parser port reproduces systemd.exec(5) quoting examples"
else
  fail "parser port self-check"
fi

# ── per-provider runs ───────────────────────────────────────────────────────
run_fetch() { # run_fetch PROVIDER OUTFILE -> stdout/stderr captured in $WORK/<p>.{out,err}
  local p="$1" f="$2" rc=0
  env -i PATH="$PATH" HOME="$WORK" STUB_FIXTURES="$FIX" \
    ITERVOX_SECRETS_PROVIDER="$p" ITERVOX_SECRETS_FILE="$f" ITERVOX_SECRETS_MAP="$MAP" \
    ITERVOX_SECRETS_GCP_PROJECT=proj ITERVOX_SECRETS_AWS_REGION=us-east-1 \
    ITERVOX_SECRETS_AZURE_VAULT=vault \
    bash "$SCRIPT" >"$WORK/$p.out" 2>"$WORK/$p.err" || rc=$?
  return "$rc"
}

for p in gcp aws azure; do
  f="$WORK/run/$p/env"
  if run_fetch "$p" "$f"; then pass "$p: exit 0"; else fail "$p: exit non-zero ($(cat "$WORK/$p.err"))"; continue; fi

  parse_envfile "$f" "$WORK/parsed-$p"
  bad=""
  for pair in $MAP; do
    k="${pair%%=*}"; n="${pair#*=}"
    if ! cmp -s "$FIX/$n" "$WORK/parsed-$p/$k"; then bad+=" $k"; fi
  done
  if [[ -z "$bad" ]]; then pass "$p: all 8 values round-trip through systemd parsing"; else fail "$p: mismatched:$bad"; fi

  mode="$(file_mode "$f")"
  if [[ "$mode" == "600" ]]; then pass "$p: file mode 0600"; else fail "$p: file mode $mode"; fi

  first="$(cat "$f")"
  run_fetch "$p" "$f" || true
  if [[ "$(cat "$f")" == "$first" ]] && [[ "$(file_mode "$f")" == "600" ]]; then
    pass "$p: re-run is byte-identical and stays 0600"
  else
    fail "$p: re-run changed the file"
  fi
  if [[ -z "$(find "$(dirname "$f")" -mindepth 1 ! -name env -print -quit)" ]]; then
    pass "$p: no temp files left behind"
  else
    fail "$p: leftover files: $(ls -A "$(dirname "$f")")"
  fi

  # A failing re-run (secret missing) must not truncate or replace the file.
  if env -i PATH="$PATH" HOME="$WORK" STUB_FIXTURES="$FIX" \
      ITERVOX_SECRETS_PROVIDER="$p" ITERVOX_SECRETS_FILE="$f" ITERVOX_SECRETS_MAP="$MAP K_MISSING=no-such-secret" \
      ITERVOX_SECRETS_GCP_PROJECT=proj ITERVOX_SECRETS_AZURE_VAULT=vault \
      bash "$SCRIPT" >"$WORK/$p.fail.out" 2>"$WORK/$p.fail.err"; then
    fail "$p: missing secret did not fail"
  elif [[ "$(cat "$f")" == "$first" ]]; then
    pass "$p: failed re-run exits non-zero and leaves the previous file intact"
  else
    fail "$p: failed re-run modified the file"
  fi

  leaks=""
  for s in "$WORK/$p.out" "$WORK/$p.err" "$WORK/$p.fail.out" "$WORK/$p.fail.err"; do
    if grep -q SENTINEL "$s"; then leaks+=" $(basename "$s")"; fi
  done
  if [[ -z "$leaks" && ! -s "$WORK/$p.out" ]]; then
    pass "$p: no secret on stdout/stderr (stdout empty; stderr: $(tr '\n' ' ' < "$WORK/$p.fail.err"))"
  else
    fail "$p: secret leaked in:$leaks"
  fi
done

# FETCH_SECRETS_TEST_KEEP=<dir> keeps the gcp output and the fixtures, for
# cross-checking this parser port against a real systemd.
if [[ -n "${FETCH_SECRETS_TEST_KEEP:-}" ]]; then
  mkdir -p "$FETCH_SECRETS_TEST_KEEP" && cp -R "$WORK/run/gcp/env" "$FIX" "$FETCH_SECRETS_TEST_KEEP/"
fi

if cmp -s "$WORK/run/gcp/env" "$WORK/run/aws/env" &&cmp -s "$WORK/run/gcp/env" "$WORK/run/azure/env"; then
  pass "gcp/aws/azure produce identical files for identical secrets"
else
  fail "providers disagree"
fi

# No provider: no-op, no file.
if env -i PATH="$PATH" ITERVOX_SECRETS_FILE="$WORK/none/env" bash "$SCRIPT" && [[ ! -e "$WORK/none/env" ]]; then
  pass "unset provider is a no-op (exit 0, no file)"
else
  fail "unset provider"
fi

# Legacy per-key variables still work.
if env -i PATH="$PATH" STUB_FIXTURES="$FIX" ITERVOX_SECRETS_PROVIDER=aws ITERVOX_SECRETS_FILE="$WORK/legacy/env" \
    ITERVOX_SECRETS_AWS_LINEAR_SECRET=s-plain ITERVOX_SECRETS_AWS_ANTHROPIC_SECRET=s-quotes bash "$SCRIPT" 2>/dev/null \
   && parse_envfile "$WORK/legacy/env" "$WORK/legacy.out" \
   && cmp -s "$FIX/s-plain" "$WORK/legacy.out/LINEAR_API_KEY" && cmp -s "$FIX/s-quotes" "$WORK/legacy.out/ANTHROPIC_API_KEY"; then
  pass "legacy ITERVOX_SECRETS_AWS_{LINEAR,ANTHROPIC}_SECRET map to LINEAR_API_KEY/ANTHROPIC_API_KEY"
else
  fail "legacy variables"
fi

if [[ $fails -eq 0 ]]; then
  echo "fetch-secrets_test: PASS"
else
  echo "fetch-secrets_test: $fails FAILED"
  exit 1
fi
