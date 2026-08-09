#!/usr/bin/env bash
#
# Provision a GCP Compute Engine VM for itervox.
#
# Defaults to NO public IP. Access is via IAP TCP tunnelling, which is gated by
# Google IAM — strictly stronger than a bearer token, and it avoids the
# loopback-behind-proxy auth gap (issue #48) entirely.
#
# Usage: ./provision.sh [--name itervox] [--zone us-central1-a] [--public]
#
set -euo pipefail

NAME="itervox"
ZONE="us-central1-a"
MACHINE="e2-standard-4"
DISK_GB=100
PUBLIC=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)    NAME="$2"; shift 2 ;;
    --zone)    ZONE="$2"; shift 2 ;;
    --machine) MACHINE="$2"; shift 2 ;;
    --disk)    DISK_GB="$2"; shift 2 ;;
    --public)  PUBLIC=true; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

NETWORK_ARGS=(--no-address)
if $PUBLIC; then NETWORK_ARGS=(); fi

echo "==> creating instance $NAME in $ZONE"
gcloud compute instances create "$NAME" \
  --zone="$ZONE" \
  --machine-type="$MACHINE" \
  --image-family=debian-12 \
  --image-project=debian-cloud \
  --boot-disk-size="${DISK_GB}GB" \
  --boot-disk-type=pd-balanced \
  --scopes=cloud-platform \
  "${NETWORK_ARGS[@]}"

# IAP tunnelling and Cloud NAT egress both need explicit plumbing when the VM
# has no external address.
if ! $PUBLIC; then
  echo "==> allowing IAP ingress on 22 (tunnel source range is fixed by Google)"
  gcloud compute firewall-rules create allow-iap-ssh \
    --allow=tcp:22 \
    --source-ranges=35.235.240.0/20 \
    --description="IAP TCP forwarding" 2>/dev/null \
    || echo "    (rule already exists)"

  echo
  echo "    NOTE: with --no-address the VM has no outbound internet access until"
  echo "    you attach a Cloud NAT gateway. bootstrap.sh needs egress to fetch"
  echo "    packages and the itervox release:"
  echo
  echo "      gcloud compute routers create itervox-router --network=default --region=\${ZONE%-*}"
  echo "      gcloud compute routers nats create itervox-nat --router=itervox-router \\"
  echo "          --region=\${ZONE%-*} --auto-allocate-nat-external-ip \\"
  echo "          --nat-all-subnet-ip-ranges"
fi

cat <<EOF

────────────────────────────────────────────────────────────────
 VM created.

 Bootstrap:
   gcloud compute scp --recurse --tunnel-through-iap --zone=$ZONE \\
     deploy/ $NAME:~/deploy
   gcloud compute ssh $NAME --tunnel-through-iap --zone=$ZONE \\
     --command 'sudo ~/deploy/bootstrap.sh --repo <git-url>'

 Reach the dashboard (no public IP, IAM-gated):
   gcloud compute start-iap-tunnel $NAME 8090 \\
     --local-host-port=localhost:8090 --zone=$ZONE
   open http://localhost:8090/?token=<ITERVOX_API_TOKEN>

 Logging + alerting: install the Ops Agent, then see deploy/monitoring/
   curl -sSO https://dl.google.com/cloudagents/add-google-cloud-ops-agent-repo.sh
   sudo bash add-google-cloud-ops-agent-repo.sh --also-install
────────────────────────────────────────────────────────────────
EOF
