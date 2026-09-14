#!/usr/bin/env bash
#
# Provision an Azure VM for itervox.
#
# Defaults to NO public IP. Access is via an Azure Bastion tunnel, gated by
# Entra ID / RBAC — stronger than a bearer token, and it avoids the
# loopback-behind-proxy auth gap (issue #48).
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

cat <<EOF

────────────────────────────────────────────────────────────────
 VM: $NAME  ($GROUP / $LOCATION)

 A VM with no public IP needs one of:
   • Azure Bastion in the VNet          (recommended — tunnel + SSH)
   • a NAT gateway for outbound egress  (bootstrap.sh needs to reach the
     internet to fetch packages and the itervox release)

 Create Bastion (needs an AzureBastionSubnet of /26 or larger in the VNet):
   az network bastion create -g $GROUP -n ${NAME}-bastion \\
     --public-ip-address ${NAME}-bastion-ip --vnet-name ${NAME}VNET \\
     --location $LOCATION --sku Standard

 Bootstrap:
   az network bastion ssh -g $GROUP -n ${NAME}-bastion \\
     --target-resource-id $VM_ID --auth-type ssh-key \\
     --username $ADMIN --ssh-key ~/.ssh/id_rsa
   # then, on the box:
   sudo ./deploy/bootstrap.sh --repo <git-url>

 Reach the dashboard:
   az network bastion tunnel -g $GROUP -n ${NAME}-bastion \\
     --target-resource-id $VM_ID --resource-port 8090 --port 8090
   open http://localhost:8090/?token=<ITERVOX_API_TOKEN>

 Logging + alerting: install the Azure Monitor agent, then see deploy/monitoring/
   az vm extension set -g $GROUP --vm-name $NAME \\
     --name AzureMonitorLinuxAgent --publisher Microsoft.Azure.Monitor
────────────────────────────────────────────────────────────────
EOF
