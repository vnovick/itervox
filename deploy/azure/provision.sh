#!/usr/bin/env bash
#
# Provision an Azure VM for itervox.
#
# Defaults to NO public IP; a NAT gateway is attached to the VM subnet for
# outbound egress (bootstrap.sh needs it to fetch packages and the itervox
# release). Access is via Tailscale (default) or an Azure Bastion Standard
# SSH tunnel with --enable-tunneling, gated by Entra ID / RBAC — strictly
# stronger than a bearer token, since it doesn't depend on the daemon's own
# auth at all (see the closed https://github.com/vnovick/itervox/issues/48:
# itervox now auto-generates a token on every bind by default). Bastion's
# tunnel only relays SSH/RDP, not arbitrary web servers, so 8090 is reached
# by forwarding it locally over the SSH tunnel, never by tunneling 8090
# directly.
#
# Usage: ./provision.sh [--name itervox] [--group itervox-rg] [--location eastus]
#
set -euo pipefail

NAME="itervox"
GROUP="itervox-rg"
LOCATION="eastus"
SIZE="Standard_D4s_v5"
DISK_GB=100
ADMIN="itervoxadmin"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)     NAME="$2"; shift 2 ;;
    --group)    GROUP="$2"; shift 2 ;;
    --location) LOCATION="$2"; shift 2 ;;
    --size)     SIZE="$2"; shift 2 ;;
    --disk)     DISK_GB="$2"; shift 2 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

echo "==> resource group $GROUP"
az group create -n "$GROUP" -l "$LOCATION" -o none

echo "==> creating VM $NAME (no public IP)"
az vm create \
  -g "$GROUP" -n "$NAME" \
  --image Ubuntu2404 \
  --size "$SIZE" \
  --admin-username "$ADMIN" \
  --generate-ssh-keys \
  --os-disk-size-gb "$DISK_GB" \
  --storage-sku Premium_LRS \
  --public-ip-address "" \
  --nsg-rule NONE \
  --assign-identity \
  -o none

VM_ID="$(az vm show -g "$GROUP" -n "$NAME" --query id -o tsv)"

# Managed identity lets the Azure Monitor agent push logs and custom metrics
# without a stored credential.
echo "==> granting the VM identity monitoring-publisher rights"
PRINCIPAL_ID="$(az vm show -g "$GROUP" -n "$NAME" --query identity.principalId -o tsv)"
az role assignment create \
  --assignee "$PRINCIPAL_ID" \
  --role "Monitoring Metrics Publisher" \
  --scope "$VM_ID" -o none 2>/dev/null || echo "    (assignment already exists)"

# CORE-062: create and attach persistent managed disk for state roots
echo "==> creating persistent managed disk for state roots"
az disk create \
  -g "$GROUP" \
  -n "${NAME}-data" \
  --size-gb 100 \
  --sku Premium_LRS \
  --location "$LOCATION" \
  -o none

# Attach the disk to the VM
az vm disk attach \
  -g "$GROUP" \
  --vm-name "$NAME" \
  --name "${NAME}-data" \
  --lun 0 \
  -o none

# ── NAT gateway for outbound egress ─────────────────────────────────────────
# `az vm create` without --vnet-name/--subnet creates <name>VNET/<name>Subnet.
# A subnet holds at most one NAT gateway association, so only associate when
# the VM subnet doesn't already have one — never replace an existing
# association on a reused subnet or resource group.
VNET="${NAME}VNET"
SUBNET="${NAME}Subnet"
echo "==> ensuring NAT gateway egress on $SUBNET"
EXISTING_NAT="$(az network vnet subnet show -g "$GROUP" --vnet-name "$VNET" -n "$SUBNET" \
  --query natGateway.id -o tsv 2>/dev/null || true)"
if [[ -z "$EXISTING_NAT" || "$EXISTING_NAT" == "None" ]]; then
  az network public-ip create -g "$GROUP" -n "${NAME}-nat-ip" \
    --sku Standard --location "$LOCATION" -o none
  az network nat gateway create -g "$GROUP" -n "${NAME}-natgw" \
    --location "$LOCATION" --public-ip-addresses "${NAME}-nat-ip" -o none
  az network vnet subnet update -g "$GROUP" --vnet-name "$VNET" -n "$SUBNET" \
    --nat-gateway "${NAME}-natgw" -o none
else
  echo "    ($SUBNET already has a NAT gateway association — leaving it alone)"
fi

cat <<EOF

────────────────────────────────────────────────────────────────
 VM: $NAME  ($GROUP / $LOCATION)

 Outbound egress: NAT gateway attached to $SUBNET (bootstrap.sh needs it to
 fetch packages and the itervox release).

 Access — pick one:
   • Tailscale (recommended default): install on the VM, bind itervox to the
     tailnet IP, and skip Bastion/public IP entirely.
   • Azure Bastion Standard with --enable-tunneling. Bastion's tunnel only
     relays SSH/RDP, never arbitrary web servers, so 8090 must be reached by
     forwarding it locally over an SSH tunnel — never by tunneling 8090
     directly (Microsoft: Bastion "doesn't relay web servers"). Needs a
     Standard public IP and an AzureBastionSubnet of /26 or larger:

     az network public-ip create -g $GROUP -n ${NAME}-bastion-ip --sku Standard -o none
     az network vnet subnet create -g $GROUP --vnet-name $VNET \\
       --name AzureBastionSubnet --address-prefixes 10.0.250.0/26 -o none
     az network bastion create -g $GROUP -n ${NAME}-bastion \\
       --public-ip-address ${NAME}-bastion-ip --vnet-name $VNET \\
       --location $LOCATION --sku Standard --enable-tunneling -o none

     az network bastion tunnel -g $GROUP -n ${NAME}-bastion \\
       --target-resource-id $VM_ID --resource-port 22 --port 2222 &
     ssh -p 2222 -L 8090:localhost:8090 $ADMIN@127.0.0.1
     open http://localhost:8090/?token=<ITERVOX_API_TOKEN>

 Bootstrap (once connected via Tailscale ssh or the tunnel above):
   sudo ./deploy/bootstrap.sh --repo <git-url> --data-disk /dev/disk/azure/scsi1/lun0
   (never /dev/sdb: on Azure that is the ephemeral resource disk)

 Logging + alerting: install the Azure Monitor agent, then see deploy/monitoring/
   az vm extension set -g $GROUP --vm-name $NAME \\
     --name AzureMonitorLinuxAgent --publisher Microsoft.Azure.Monitor
────────────────────────────────────────────────────────────────
EOF
