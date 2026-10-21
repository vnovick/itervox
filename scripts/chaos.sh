#!/usr/bin/env bash
# chaos.sh — run Go test packages over and over while busy loops saturate
# every CPU (#126). The orchestrator's races only showed up when the event
# loop, a worker and a reconcile tick were starved in an unlucky order, which
# a single `go test` run on an idle machine almost never produces.
#
# Every package's test binary is built once (-race -cover) and run
# CHAOS_COUNT times per CPU setting in CHAOS_CPU, while CHAOS_HOGS busy loops
# (default: one per CPU) compete for the CPUs. A package that fails keeps its
# full verbose log in CHAOS_OUT; a test that hangs hits CHAOS_TIMEOUT, and the
# timeout panic prints every goroutine's stack into that log.
#
# Usage: make chaos  (or scripts/chaos.sh), configured by environment:
#   CHAOS_PACKAGES  packages to run      (default: ./internal/orchestrator/... ./internal/tracker/...)
#   CHAOS_RUN       -test.run regex      (default: all tests)
#   CHAOS_COUNT     runs per CPU setting (default: 5)
#   CHAOS_CPU       -test.cpu list       (default: 1,2,4)
#   CHAOS_HOGS      busy loops           (default: number of CPUs)
#   CHAOS_TIMEOUT   per-package timeout  (default: 90m)
#   CHAOS_OUT       log directory        (default: chaos-out)
set -euo pipefail

packages=${CHAOS_PACKAGES:-./internal/orchestrator/... ./internal/tracker/...}
run=${CHAOS_RUN:-.}
count=${CHAOS_COUNT:-5}
cpus=${CHAOS_CPU:-1,2,4}
hogs=${CHAOS_HOGS:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)}
timeout=${CHAOS_TIMEOUT:-90m}
out=${CHAOS_OUT:-chaos-out}

mkdir -p "$out/bin"
out=$(cd "$out" && pwd)

# Many tests commit in throwaway repositories. A developer's global git
# config may sign every commit (an agent or key prompt per commit, thousands
# of times here), so the tests get a minimal config of their own.
gitconfig="$out/gitconfig"
printf '[user]\n\tname = itervox-chaos\n\temail = chaos@example.invalid\n[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n' >"$gitconfig"
export GIT_CONFIG_GLOBAL="$gitconfig"
# A timed-out test panics; dump every goroutine, not only the panicking one.
export GOTRACEBACK=all

hog_pids=()
stop_hogs() {
  if ((${#hog_pids[@]})); then
    kill "${hog_pids[@]}" 2>/dev/null || true
    wait "${hog_pids[@]}" 2>/dev/null || true
  fi
}
trap stop_hogs EXIT

# Build first, without the hogs: a slow build is not what is being tested.
# $packages is a space-separated list, split on purpose.
read -r -a pkg_args <<<"$packages"
mapfile -t pkgs < <(go list "${pkg_args[@]}")
built=()
for pkg in "${pkgs[@]}"; do
  name=${pkg//\//_}
  # -c writes no binary for a package without tests.
  go test -race -cover -c -o "$out/bin/$name.test" "$pkg"
  if [[ -x "$out/bin/$name.test" ]]; then
    built+=("$pkg")
  fi
done

for ((i = 0; i < hogs; i++)); do
  (while :; do :; done) &
  hog_pids+=($!)
done
echo "chaos: ${#built[@]} package(s), -count=$count -cpu=$cpus, $hogs busy loop(s), run=$run"

failed=()
for pkg in "${built[@]}"; do
  name=${pkg//\//_}
  log="$out/$name.log"
  dir=$(go list -f '{{.Dir}}' "$pkg")
  echo "chaos: $pkg"
  # The binary runs in its package directory, as `go test` runs it, so
  # testdata paths resolve.
  if (cd "$dir" && "$out/bin/$name.test" -test.run "$run" -test.count "$count" \
    -test.cpu "$cpus" -test.timeout "$timeout" -test.v) >"$log" 2>&1; then
    rm -f "$log"
  else
    failed+=("$pkg")
    echo "chaos: FAIL $pkg (log: $log)"
    grep -E '^(--- FAIL|panic:|FAIL)' "$log" | sort | uniq -c | head -20 || true
  fi
done
rm -rf "${out:?}/bin" "$gitconfig"

if ((${#failed[@]})); then
  echo "chaos: ${#failed[@]} package(s) failed: ${failed[*]}"
  exit 1
fi
echo "chaos: all packages passed"
