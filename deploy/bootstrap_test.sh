#!/usr/bin/env bash
#
# CORE-062 test for deploy/bootstrap.sh's data-disk and HOME-relocation steps.
# Run: make deploy-test (or bash deploy/bootstrap_test.sh). No root needed:
# the script is sourced with --functions-only and blkid, findmnt, mkfs.ext4,
# mount, umount and systemctl are stubs on PATH that record every call.
#
# Proves:
#   - a disk that already has a filesystem is NEVER formatted (dry-run and
#     real mode), and neither is a partitioned disk, an unclassifiable disk,
#     or one whose mount point already holds data;
#   - only a disk with no signature at all is formatted, exactly once;
#   - the fstab entry is `UUID=<uuid> <root> <fstype> defaults,nofail,... 0 2`,
#     written once (idempotent re-run) and never alongside a second entry
#     for the same mount point;
#   - migrate_home carries every HOME state root over and repoints the account;
#   - (M4-close) GH_TOKEN never appears in argv or the --dry-run echo, and
#     reaches both gh setup-git and the HTTPS clone through sudo
#     --preserve-env; --git-name is one argv word; .env is 0600 from
#     creation; an unopenable device (blkid -p exit 2 + error) is refused.

# Stub bodies and the bash -c scripts below are single-quoted on purpose:
# they expand in the child process, not here.
# shellcheck disable=SC2016
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOOTSTRAP="$HERE/bootstrap.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

mkdir -p "$WORK/bin"
CALLS="$WORK/calls.log"
mkstub() { printf '#!/usr/bin/env bash\n%s\n' "$2" > "$WORK/bin/$1"; chmod +x "$WORK/bin/$1"; }

# blkid: BLKID_MODE=ext4|empty|gpt|error. "empty" turns into an ext4 with a
# NEW uuid once mkfs.ext4 has run (the stub drops $STATE/formatted).
mkstub blkid 'echo "blkid $*" >> "$CALLS"
if [[ -f "$STATE/formatted" ]]; then printf "DEVNAME=%s\nUUID=2222-bbbb\nTYPE=ext4\n" "${*: -1}"; exit 0; fi
case "$BLKID_MODE" in
  ext4)  printf "DEVNAME=%s\nUUID=1111-aaaa\nBLOCK_SIZE=4096\nTYPE=ext4\n" "${*: -1}" ;;
  xfs)   printf "DEVNAME=%s\nUUID=3333-cccc\nTYPE=xfs\n" "${*: -1}" ;;
  empty) exit 2 ;;
  gpt)   printf "DEVNAME=%s\nPTUUID=abcd\nPTTYPE=gpt\n" "${*: -1}" ;;
  error) exit 4 ;;
esac'
mkstub mkfs.ext4 'echo "mkfs.ext4 $*" >> "$CALLS"; touch "$STATE/formatted"'
mkstub mount     'echo "mount $*" >> "$CALLS"'
# umount: record what was written to the (stub-)mounted dir, then empty it —
# after a real umount the copied files live on the disk, not in the dir.
mkstub umount    'echo "umount $*" >> "$CALLS"; d="${*: -1}"
echo "seeded-content $(ls -A "$d" | tr "\n" " ")" >> "$CALLS"
find "$d" -mindepth 1 -delete'
mkstub findmnt   'echo "findmnt $*" >> "$CALLS"; [[ -n "${FINDMNT_TARGET:-}" ]] && echo "$FINDMNT_TARGET"; exit 0'
mkstub systemctl 'echo "systemctl $*" >> "$CALLS"; [[ "$*" == *is-active* ]] && { [[ "${SVC_ACTIVE:-0}" == 1 ]]; exit $?; }; exit 0'
mkstub usermod   'echo "usermod $*" >> "$CALLS"'

# run_case NAME MODE DRY [extra env...] — runs setup_data_disk in a fresh
# sourced shell; sets RC, OUT and a fresh CALLS/fstab per case.
run_case() {
  local name="$1" mode="$2" dry="$3"; shift 3
  CASE="$WORK/$name"; mkdir -p "$CASE/state"
  : > "$CALLS"
  [[ -f "$CASE/fstab" ]] || printf '# /etc/fstab\nUUID=0000-root / ext4 defaults 0 1\n' > "$CASE/fstab"
  [[ -e "$CASE/disk" ]] || : > "$CASE/disk"
  RC=0
  OUT="$(env PATH="$WORK/bin:$PATH" CALLS="$CALLS" STATE="$CASE/state" BLKID_MODE="$mode" "$@" \
    bash -c '
      set -euo pipefail
      # shellcheck source=/dev/null
      source "$1" --functions-only
      DATA_DISK="$2"; ROOT="$3"; FSTAB="$4"; DRY_RUN="$5"
      setup_data_disk
    ' _ "$BOOTSTRAP" "$CASE/disk" "$CASE/root" "$CASE/fstab" "$dry" 2>&1)" || RC=$?
}
called() { grep -q "^$1" "$CALLS"; }
fstab_lines() { grep -c "$1" "$CASE/fstab" || true; }

# 1. existing ext4, dry-run
run_case ext4-dry ext4 true
if [[ $RC -eq 0 ]] && ! called mkfs && ! grep -q 'mkfs' <<< "$OUT" \
   && grep -q "\[dry-run\] bash -c printf '%s\\\\n' 'UUID=1111-aaaa $CASE/root ext4 defaults,nofail,x-systemd.device-timeout=30s 0 2'" <<< "$OUT" \
   && [[ "$(fstab_lines UUID=1111-aaaa)" == 0 ]]; then
  pass "dry-run, disk has ext4: no mkfs (run or printed); prints the UUID fstab line; fstab untouched"
else
  fail "dry-run ext4 (rc=$RC): $OUT"
fi

# 2. existing ext4, real mode (stubbed), then an idempotent re-run
run_case ext4-real ext4 false
want="UUID=1111-aaaa $CASE/root ext4 defaults,nofail,x-systemd.device-timeout=30s 0 2"
if [[ $RC -eq 0 ]] && ! called mkfs && grep -qxF "$want" "$CASE/fstab" \
   && grep -qxF "mount -T $CASE/fstab $CASE/root" "$CALLS"; then
  pass "disk has ext4: never formatted; fstab gets '$want'; mounted through fstab"
else
  fail "real ext4 (rc=$RC): $OUT / $(cat "$CALLS")"
fi
run_case ext4-real ext4 false FINDMNT_TARGET="$WORK/ext4-real/root"
if [[ $RC -eq 0 ]] && ! called mkfs && [[ "$(fstab_lines UUID=1111-aaaa)" == 1 ]] && ! called 'mount '; then
  pass "re-run: fstab still has exactly one entry, already-mounted disk is not remounted"
else
  fail "re-run (rc=$RC): $(cat "$CASE/fstab")"
fi

# 3. xfs is reused with its own type
run_case xfs-real xfs false
if [[ $RC -eq 0 ]] && ! called mkfs && grep -qxF "UUID=3333-cccc $CASE/root xfs defaults,nofail,x-systemd.device-timeout=30s 0 2" "$CASE/fstab"; then
  pass "existing xfs is reused as xfs (type comes from blkid, not assumed)"
else
  fail "xfs (rc=$RC): $OUT"
fi

# 4. blank disk, dry-run: mkfs only printed
run_case empty-dry empty true
if [[ $RC -eq 0 ]] && ! called mkfs && grep -q '\[dry-run\] mkfs.ext4 -q -L itervox-data' <<< "$OUT"; then
  pass "dry-run, blank disk: mkfs is printed, not run"
else
  fail "dry-run empty (rc=$RC): $OUT"
fi

# 5. blank disk, real: formatted once, fstab uses the NEW uuid
run_case empty-real empty false
if [[ $RC -eq 0 ]] && [[ "$(grep -c '^mkfs.ext4' "$CALLS")" == 1 ]] \
   && grep -qxF "mkfs.ext4 -q -L itervox-data $(readlink -f "$CASE/disk")" "$CALLS" \
   && grep -qxF "UUID=2222-bbbb $CASE/root ext4 defaults,nofail,x-systemd.device-timeout=30s 0 2" "$CASE/fstab"; then
  pass "blank disk (blkid -p exit 2): formatted exactly once; fstab uses the post-mkfs UUID"
else
  fail "real empty (rc=$RC): $OUT / $(cat "$CALLS")"
fi

# 6-8. refusals: partition table, blkid error, fstab conflict — no mkfs, fstab untouched
run_case gpt gpt false
if [[ $RC -ne 0 ]] && ! called mkfs && grep -q 'partition table' <<< "$OUT" && [[ "$(wc -l < "$CASE/fstab")" -eq 2 ]]; then
  pass "partitioned disk: refused, no mkfs, fstab untouched"
else
  fail "gpt (rc=$RC): $OUT"
fi
run_case blkid-err error false
if [[ $RC -ne 0 ]] && ! called mkfs && [[ "$(wc -l < "$CASE/fstab")" -eq 2 ]]; then
  pass "blkid failure: refused, no mkfs, fstab untouched"
else
  fail "blkid error (rc=$RC): $OUT"
fi
mkdir -p "$WORK/conflict"
printf 'UUID=9999 %s ext4 defaults 0 2\n' "$WORK/conflict/root" > "$WORK/conflict/fstab"
run_case conflict empty false
if [[ $RC -ne 0 ]] && ! called mkfs && grep -q 'already mounts something else' <<< "$OUT"; then
  pass "fstab already mounts another device at the root: refused before mkfs"
else
  fail "fstab conflict (rc=$RC): $OUT"
fi

# 9. existing fs + non-empty root: refused (mounting would hide data)
mkdir -p "$WORK/busy-fs/root/home"
run_case busy-fs ext4 false
if [[ $RC -ne 0 ]] && ! called mkfs && ! called 'mount ' && [[ "$(wc -l < "$CASE/fstab")" -eq 2 ]]; then
  pass "disk has a filesystem and the root already has data: refused, nothing written"
else
  fail "busy fs (rc=$RC): $OUT"
fi

# 10. blank disk + non-empty root: formatted, seeded from the root, then mounted
mkdir -p "$WORK/busy-empty/root/home/.itervox"
run_case busy-empty empty false
seed_order="$(grep -nE '^(mkfs|mount|umount|seeded-content)' "$CALLS" | cut -d: -f2- | tr '\n' '|')"
if [[ $RC -eq 0 ]] && [[ "$seed_order" == "mkfs.ext4 "*"|mount $(readlink -f "$CASE/disk") $CASE/root.seed|umount $CASE/root.seed|seeded-content home |mount -T $CASE/fstab $CASE/root|" ]] \
   && [[ ! -e "$CASE/root.seed" && -d "$CASE/root/home/.itervox" ]]; then
  pass "blank disk, root already populated: mkfs -> seed copy -> umount -> mount via fstab"
else
  fail "busy empty (rc=$RC): $seed_order"
fi

# 11. mounted elsewhere
run_case elsewhere ext4 false FINDMNT_TARGET=/mnt/other
if [[ $RC -ne 0 ]] && ! called mkfs && grep -q 'already mounted at /mnt/other' <<< "$OUT"; then
  pass "disk mounted elsewhere: refused"
else
  fail "elsewhere (rc=$RC): $OUT"
fi

# 12. HOME relocation carries every state root and repoints the account
OLD="$WORK/oldhome"; NEW="$WORK/newhome"
mkdir -p "$OLD/.itervox/logs/p" "$OLD/.itervox/workspaces" "$OLD/.claude" "$OLD/.codex" "$OLD/.config/gh" "$OLD/.ssh"
: > "$OLD/.claude.json"; : > "$OLD/.gitconfig"; : > "$OLD/.itervox/logs/p/api-token"
: > "$CALLS"
RC=0
OUT="$(env PATH="$WORK/bin:$PATH" CALLS="$CALLS" bash -c '
  set -euo pipefail
  # shellcheck source=/dev/null
  source "$1" --functions-only
  SVC_USER=itervox; DRY_RUN=true
  migrate_home "$2" "$3"
' _ "$BOOTSTRAP" "$OLD" "$NEW" 2>&1)" || RC=$?
missing=""
for p in .itervox .claude .claude.json .codex .config/gh .ssh .gitconfig; do
  grep -qF "cp -a --parents '$p' '$NEW/'" <<< "$OUT" || missing+=" $p"
done
if [[ $RC -eq 0 && -z "$missing" ]] && grep -qF "[dry-run] usermod -d $NEW itervox" <<< "$OUT"; then
  pass "migrate_home copies .itervox (workspaces, logs, api-token) .claude .claude.json .codex .config/gh .ssh .gitconfig and runs usermod -d"
else
  fail "migrate_home (rc=$RC) missing:$missing / $OUT"
fi
RC=0
OUT="$(env PATH="$WORK/bin:$PATH" CALLS="$CALLS" SVC_ACTIVE=1 bash -c '
  set -euo pipefail
  # shellcheck source=/dev/null
  source "$1" --functions-only
  SVC_USER=itervox; DRY_RUN=true
  migrate_home "$2" "$3"
' _ "$BOOTSTRAP" "$OLD" "$NEW" 2>&1)" || RC=$?
if [[ $RC -ne 0 ]] && grep -q 'systemctl stop itervox' <<< "$OUT"; then
  pass "migrate_home refuses while the service is running"
else
  fail "migrate_home while active (rc=$RC): $OUT"
fi

# ── M4-close: repository access, clone, identity, .env, unreadable disk ────
# sudo stub: records its argv, then behaves like sudo's env_reset — the
# command sees only PATH/CALLS/STATE plus what --preserve-env names.
mkstub sudo 'echo "sudo $*" >> "$CALLS"
keep=("PATH=$PATH" "CALLS=$CALLS" "STATE=${STATE:-}")
while [[ $# -gt 0 ]]; do
  case "$1" in
    --preserve-env=*)
      IFS=, read -ra names <<< "${1#--preserve-env=}"
      for n in "${names[@]}"; do
        if [[ -n "${!n+x}" ]]; then keep+=("$n=${!n}"); fi
      done
      shift ;;
    -u) shift 2 ;;
    -H) shift ;;
    --) shift; break ;;
    -*) shift ;;
    *) break ;;
  esac
done
exec env -i "${keep[@]}" "$@"'
mkstub gh      'echo "gh $* GH_TOKEN=${GH_TOKEN:+set}" >> "$CALLS"'
mkstub git     'printf "git-argv" >> "$CALLS"; printf " [%s]" "$@" >> "$CALLS"; echo " GH_TOKEN=${GH_TOKEN:+set}" >> "$CALLS"'
mkstub chown   'echo "chown $*" >> "$CALLS"'
# openssl rand runs while the heredoc is expanded, i.e. after the > redirection
# created .env: record the file mode the token is written into.
mkstub openssl 'echo "env-mode-at-write $(ls -l "$ENV_PROBE" | cut -c1-10)" >> "$CALLS"; echo 0123456789abcdef'

TOKEN="ghp_M4closeSENTINEL0123456789abcdefABCDEF"

# run_fn DRY NETDRY SCRIPT [env...] — sources bootstrap.sh --functions-only
# and runs SCRIPT (bash) with the stubs; sets RC and OUT.
run_fn() {
  local dry="$1" netdry="$2" script="$3"; shift 3
  CASE="$WORK/fn-$RANDOM"; mkdir -p "$CASE/state" "$CASE/work/.itervox"
  : > "$CALLS"
  RC=0
  OUT="$(env PATH="$WORK/bin:$PATH" CALLS="$CALLS" STATE="$CASE/state" ENV_PROBE="$CASE/work/.itervox/.env" "$@" \
    bash -c '
      set -euo pipefail
      # shellcheck source=/dev/null
      source "$1" --functions-only
      DRY_RUN="$2"; DRY_RUN_NETWORK="$3"; SVC_USER=itervox; WORKDIR="$4/work"
      eval "$5"
    ' _ "$BOOTSTRAP" "$dry" "$netdry" "$CASE" "$script" 2>&1)" || RC=$?
}

# 13. BH-M4-4: GH_TOKEN never in argv and never in the --dry-run echo
for mode in "true false" "false true"; do
  read -r dry netdry <<< "$mode"
  run_fn "$dry" "$netdry" 'REPO=https://github.com/example/private.git; setup_repo_access; clone_repo' GH_TOKEN="$TOKEN"
  if [[ $RC -eq 0 ]] && ! grep -qF "$TOKEN" <<< "$OUT" && ! grep -qF "$TOKEN" "$CALLS" \
     && grep -qF -- "[dry-run] sudo --preserve-env=GH_TOKEN -u itervox gh auth setup-git" <<< "$OUT"; then
    pass "dry-run=$dry dry-run-network=$netdry: GH_TOKEN is not printed and not in any argv; gh gets it via --preserve-env"
  else
    fail "GH_TOKEN dry-run=$dry net=$netdry (rc=$RC): $OUT / $(cat "$CALLS")"
  fi
done
# A token that did reach a printed command line is still redacted.
run_fn true false 'run_net curl -H "Authorization: token $GH_TOKEN" https://api.github.com' GH_TOKEN="$TOKEN"
if [[ $RC -eq 0 ]] && ! grep -qF "$TOKEN" <<< "$OUT" && grep -qF 'Authorization: token ***' <<< "$OUT"; then
  pass "run_net redacts GH_TOKEN in its [dry-run] echo"
else
  fail "run_net redaction (rc=$RC): $OUT"
fi

# 14. BH-M4-4/5 real mode: argv is clean, and BOTH gh setup-git and the HTTPS
# clone see GH_TOKEN through sudo's env reset.
run_fn false false 'REPO=https://github.com/example/private.git; setup_repo_access; clone_repo' GH_TOKEN="$TOKEN"
if [[ $RC -eq 0 ]] && ! grep -qF "$TOKEN" "$CALLS" && ! grep -qF "$TOKEN" <<< "$OUT" \
   && grep -qxF "gh auth setup-git GH_TOKEN=set" "$CALLS" \
   && grep -qF "git-argv [clone] [https://github.com/example/private.git] [$CASE/work] GH_TOKEN=set" "$CALLS"; then
  pass "HTTPS repo: gh auth setup-git and git clone both receive GH_TOKEN (preserve-env), no argv carries it"
else
  fail "HTTPS clone token (rc=$RC): $OUT / $(cat "$CALLS")"
fi

# 15. --git-name with quotes and a command separator stays one argv word
NAME="O'Brien \"Bob\"; touch $WORK/pwned"
run_fn false false 'GIT_NAME="$GN"; GIT_EMAIL="bot@example.com"; set_git_identity' GN="$NAME"
if [[ $RC -eq 0 ]] && [[ ! -e "$WORK/pwned" ]] \
   && grep -qF "git-argv [-C] [$CASE/work] [config] [user.name] [$NAME]" "$CALLS" \
   && grep -qF "git-argv [-C] [$CASE/work] [config] [user.email] [bot@example.com]" "$CALLS"; then
  pass "--git-name with quotes: set verbatim as one argument, nothing executed"
else
  fail "git identity (rc=$RC): $OUT / $(cat "$CALLS")"
fi

# 16. .env is 0600 from creation, not chmod'ed after the token was written
# mode_of prints the ls -l permission string (portable across GNU and BSD).
# shellcheck disable=SC2012 # one known file, no name parsing
mode_of() { ls -l "$1" | cut -c1-10; }
run_fn false false 'scaffold_env_file'
if [[ $RC -eq 0 ]] && grep -qxF "env-mode-at-write -rw-------" "$CALLS" \
   && [[ "$(mode_of "$CASE/work/.itervox/.env")" == "-rw-------" ]] \
   && grep -q '^ITERVOX_API_TOKEN=0123456789abcdef$' "$CASE/work/.itervox/.env"; then
  pass ".env scaffold: mode 0600 while the token is written and after"
else
  fail ".env umask (rc=$RC): $OUT / $(cat "$CALLS")"
fi

# 17. D10: blkid -p exits 2 for an unopenable device too — never "empty"
cat > "$WORK/bin/blkid" <<'STUB'
#!/usr/bin/env bash
echo "blkid $*" >> "$CALLS"
if [[ "$BLKID_MODE" == unreadable ]]; then echo "blkid: error: ${*: -1}: Permission denied" >&2; exit 2; fi
if [[ -f "$STATE/formatted" ]]; then printf "DEVNAME=%s\nUUID=2222-bbbb\nTYPE=ext4\n" "${*: -1}"; exit 0; fi
case "$BLKID_MODE" in
  ext4)  printf "DEVNAME=%s\nUUID=1111-aaaa\nBLOCK_SIZE=4096\nTYPE=ext4\n" "${*: -1}" ;;
  xfs)   printf "DEVNAME=%s\nUUID=3333-cccc\nTYPE=xfs\n" "${*: -1}" ;;
  empty) exit 2 ;;
  gpt)   printf "DEVNAME=%s\nPTUUID=abcd\nPTTYPE=gpt\n" "${*: -1}" ;;
  error) exit 4 ;;
esac
STUB
chmod +x "$WORK/bin/blkid"
run_case unreadable unreadable false
if [[ $RC -ne 0 ]] && ! called mkfs && [[ "$(wc -l < "$CASE/fstab")" -eq 2 ]] && grep -q 'Permission denied' <<< "$OUT"; then
  pass "blkid -p exit 2 WITH an error (unopenable device): refused, no mkfs, fstab untouched"
else
  fail "unreadable blkid (rc=$RC): $OUT"
fi
if [[ $EUID -ne 0 ]]; then
  mkdir -p "$WORK/noperm"; : > "$WORK/noperm/disk"; chmod 000 "$WORK/noperm/disk"
  run_case noperm empty false
  if [[ $RC -ne 0 ]] && ! called mkfs && ! called blkid; then
    pass "device not readable by this user: refused before blkid, no mkfs"
  else
    fail "unreadable device (rc=$RC): $OUT / $(cat "$CALLS")"
  fi
  chmod 600 "$WORK/noperm/disk"
fi

if [[ $fails -eq 0 ]]; then
  echo "bootstrap_test: PASS"
else
  echo "bootstrap_test: $fails FAILED"
  exit 1
fi
