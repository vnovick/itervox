#!/usr/bin/env bash
#
# M4-close BH-M4-6 test for deploy/aws/provision.sh, against a stubbed `aws`
# CLI on PATH (no AWS account, no network). Run: make deploy-test.
#
# Proves:
#   - the data volume is created in the instance's ACTUAL availability zone
#     (Placement.AvailabilityZone), not a guessed "${REGION}a";
#   - the order is: instance running -> placement read -> volume created ->
#     volume available -> attached -> volume in-use;
#   - no modify-instance-attribute call (a volume attached after launch is
#     already DeleteOnTermination=false);
#   - a failure after resources exist removes exactly what this run created
#     (the volume, the instance), and a failure before anything exists
#     removes nothing.

# The stub body is single-quoted on purpose: it expands in the stub.
# shellcheck disable=SC2016
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROVISION="$HERE/provision.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

mkdir -p "$WORK/bin"
CALLS="$WORK/calls.log"
cat > "$WORK/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "aws $*" >> "$CALLS"
key="$1 $2"
if [[ -n "${AWS_FAIL:-}" && "$key" == "$AWS_FAIL" ]]; then
  echo "An error occurred (Stubbed) when calling $2" >&2
  exit 254
fi
case "$key" in
  "iam get-instance-profile")      echo '{}' ;;
  "ec2 describe-vpcs")             echo vpc-1 ;;
  "ec2 describe-security-groups")  echo sg-1 ;;
  "ssm get-parameters")            echo ami-1 ;;
  "ec2 run-instances")             echo i-0abc ;;
  "ec2 describe-instances")        echo us-east-1c ;;
  "ec2 create-volume")             echo vol-0def ;;
  *)                               : ;;
esac
STUB
chmod +x "$WORK/bin/aws"

run_provision() {
  : > "$CALLS"
  RC=0
  OUT="$(env PATH="$WORK/bin:$PATH" CALLS="$CALLS" "$@" bash "$PROVISION" --region us-east-1 2>&1)" || RC=$?
}
line_of() { { grep -n -m1 -F -- "$1" "$CALLS" || true; } | cut -d: -f1; }
called() { grep -qF -- "$1" "$CALLS"; }

# 1. success path
run_provision
az_ok=false; order_ok=false
if called "ec2 create-volume" && grep -F "ec2 create-volume" "$CALLS" | grep -qF -- "--availability-zone us-east-1c"; then
  az_ok=true
fi
l_run="$(line_of 'ec2 wait instance-running --instance-ids i-0abc')"
l_az="$(line_of 'ec2 describe-instances --instance-ids i-0abc')"
l_cv="$(line_of 'ec2 create-volume')"
l_va="$(line_of 'ec2 wait volume-available --volume-ids vol-0def')"
l_at="$(line_of 'ec2 attach-volume')"
l_iu="$(line_of 'ec2 wait volume-in-use --volume-ids vol-0def')"
if [[ -n "$l_run" && -n "$l_az" && -n "$l_cv" && -n "$l_va" && -n "$l_at" && -n "$l_iu" ]] \
   && (( l_run < l_az && l_az < l_cv && l_cv < l_va && l_va < l_at && l_at < l_iu )); then
  order_ok=true
fi
if [[ $RC -eq 0 ]] && $az_ok && $order_ok && ! called "modify-instance-attribute" \
   && ! called "terminate-instances" && ! called "delete-volume" \
   && grep -F "ec2 describe-instances" "$CALLS" | grep -qF "Placement.AvailabilityZone"; then
  pass "volume created in the instance's Placement AZ (us-east-1c, not us-east-1a); running -> AZ -> create -> available -> attach -> in-use; no modify-instance-attribute; nothing cleaned up"
else
  fail "success path (rc=$RC az_ok=$az_ok order_ok=$order_ok): $OUT / $(cat "$CALLS")"
fi

# 2. attach fails: the volume and the instance this run created are removed
run_provision AWS_FAIL="ec2 attach-volume"
if [[ $RC -ne 0 ]] && called "ec2 delete-volume --volume-id vol-0def" \
   && called "ec2 terminate-instances --instance-ids i-0abc"; then
  pass "attach-volume fails: cleanup deletes vol-0def and terminates i-0abc, exit non-zero"
else
  fail "attach failure cleanup (rc=$RC): $OUT / $(cat "$CALLS")"
fi

# 3. volume never becomes available: volume and instance removed
run_provision AWS_FAIL="ec2 wait"
if [[ $RC -ne 0 ]] && called "ec2 terminate-instances --instance-ids i-0abc"; then
  pass "a wait fails: the launched instance is terminated"
else
  fail "wait failure cleanup (rc=$RC): $OUT / $(cat "$CALLS")"
fi

# 4. run-instances fails: nothing was created, nothing is deleted
run_provision AWS_FAIL="ec2 run-instances"
if [[ $RC -ne 0 ]] && ! called "terminate-instances" && ! called "delete-volume"; then
  pass "run-instances fails: nothing created, nothing torn down"
else
  fail "early failure (rc=$RC): $OUT / $(cat "$CALLS")"
fi

if [[ $fails -eq 0 ]]; then
  echo "aws_provision_test: PASS"
else
  echo "aws_provision_test: $fails FAILED"
  exit 1
fi
