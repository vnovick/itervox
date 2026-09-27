#!/usr/bin/env bash
#
# Provision a GCP Compute Engine VM for itervox.
#
# Defaults to NO public IP. Access is via IAP TCP tunnelling, which is gated by
# Google IAM — strictly stronger than a bearer token, since it doesn't depend
# on the daemon's own auth at all (see the closed
# https://github.com/vnovick/itervox/issues/48: itervox now auto-generates a
# token on every bind by default).
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

# CORE-062: create and attach persistent data disk for state roots
echo "==> creating persistent data disk for state roots"
gcloud compute disks create "${NAME}-data" \
  --zone="$ZONE" \
  --size=100GB \
  --type=pd-balanced

gcloud compute instances attach-disk "$NAME" \
  --disk="${NAME}-data" \
  --device-name=itervox-data \
  --zone="$ZONE"

# IAP tunnelling and Cloud NAT egress both need explicit plumbing when the VM
# has no external address.
if ! $PUBLIC; then
  REGION="${ZONE%-*}"

  echo "==> allowing IAP ingress on 22 (tunnel source range is fixed by Google)"
  if ! gcloud compute firewall-rules describe allow-iap-ssh --format='value(name)' &>/dev/null; then
    gcloud compute firewall-rules create allow-iap-ssh \
      --network=default \
      --allow=tcp:22 \
      --source-ranges=35.235.240.0/20 \
      --description="IAP TCP forwarding"
  else
    echo "    (rule already exists)"
  fi

  echo "==> ensuring Cloud NAT egress (bootstrap.sh needs it to fetch packages and the itervox release)"
  if ! gcloud compute routers describe itervox-router --region="$REGION" --format='value(name)' &>/dev/null; then
    gcloud compute routers create itervox-router --network=default --region="$REGION"
  fi
  if ! gcloud compute routers nats describe itervox-nat --router=itervox-router --region="$REGION" --format='value(name)' &>/dev/null; then
    gcloud compute routers nats create itervox-nat --router=itervox-router \
      --region="$REGION" --auto-allocate-nat-external-ip \
      --nat-all-subnet-ip-ranges
  else
    echo "    (NAT already exists)"
  fi
fi

cat <<EOF

────────────────────────────────────────────────────────────────
 VM created.

 Bootstrap:
   gcloud compute scp --recurse --tunnel-through-iap --zone=$ZONE \\
     deploy/ $NAME:~/deploy
   gcloud compute ssh $NAME --tunnel-through-iap --zone=$ZONE \\
     --command 'sudo ~/deploy/bootstrap.sh --repo <git-url> --data-disk /dev/disk/by-id/google-itervox-data'

 Reach the dashboard (no public IP, IAM-gated; itervox binds 127.0.0.1:8090,
 so tunnel SSH and forward the port locally rather than tunnelling 8090 directly):
   gcloud compute ssh $NAME --tunnel-through-iap --zone=$ZONE -- -N -L 8090:localhost:8090
   open http://localhost:8090/?token=<ITERVOX_API_TOKEN>

 Logging + alerting: install the Ops Agent, then see deploy/monitoring/
   curl -sSO https://dl.google.com/cloudagents/add-google-cloud-ops-agent-repo.sh
   sudo bash add-google-cloud-ops-agent-repo.sh --also-install
────────────────────────────────────────────────────────────────
EOF
