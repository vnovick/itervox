#!/usr/bin/env bash
#
# Provision an AWS EC2 instance for itervox.
#
# Defaults to NO inbound security-group rule at all. Access is via SSM Session
# Manager port forwarding, gated by IAM — strictly stronger than a bearer
# token, since it doesn't depend on the daemon's own auth at all (see the
# closed https://github.com/vnovick/itervox/issues/48: itervox now
# auto-generates a token on every bind by default).
#
# Requires an instance profile with AmazonSSMManagedInstanceCore attached;
# the script creates one if absent.
#
# Usage: ./provision.sh [--name itervox] [--region us-east-1] [--type t3.xlarge]
#                       [--disk 100] [--data-disk-size 100]
#
# A failure after the instance or the data volume exists removes them again
# (the security group and the instance profile are reusable and stay).
#
set -euo pipefail

NAME="itervox"
REGION="${AWS_REGION:-us-east-1}"
TYPE="t3.xlarge"
DISK_GB=100
DATA_GB=100
PROFILE_NAME="ItervoxSSMInstanceProfile"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)   NAME="$2"; shift 2 ;;
    --region) REGION="$2"; shift 2 ;;
    --type)   TYPE="$2"; shift 2 ;;
    --disk)   DISK_GB="$2"; shift 2 ;;
    --data-disk-size) DATA_GB="$2"; shift 2 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

export AWS_REGION="$REGION"

# ── partial-failure cleanup (M4-close BH-M4-6) ──────────────────────────────
# Anything this run creates that is billed per hour is removed again if a
# later step fails, so a failed provision leaves no orphaned instance or
# volume behind. The security group and the IAM instance profile are
# reusable across runs and are left in place.
CREATED_INSTANCE=""
CREATED_VOLUME=""
cleanup_on_failure() {
  local rc=$?
  if [[ $rc -eq 0 ]]; then
    return 0
  fi
  if [[ -z "$CREATED_INSTANCE$CREATED_VOLUME" ]]; then
    return "$rc"
  fi
  echo "==> provisioning failed (exit $rc); removing what this run created" >&2
  if [[ -n "$CREATED_VOLUME" ]]; then
    aws ec2 detach-volume --volume-id "$CREATED_VOLUME" --force >/dev/null 2>&1 || true
    aws ec2 wait volume-available --volume-ids "$CREATED_VOLUME" >/dev/null 2>&1 || true
    aws ec2 delete-volume --volume-id "$CREATED_VOLUME" >/dev/null 2>&1 \
      || echo "warning: could not delete volume $CREATED_VOLUME — delete it by hand" >&2
  fi
  if [[ -n "$CREATED_INSTANCE" ]]; then
    aws ec2 terminate-instances --instance-ids "$CREATED_INSTANCE" >/dev/null 2>&1 \
      || echo "warning: could not terminate $CREATED_INSTANCE — terminate it by hand" >&2
  fi
  return "$rc"
}
trap cleanup_on_failure EXIT

# ── instance profile for SSM ────────────────────────────────────────────────
if ! aws iam get-instance-profile --instance-profile-name "$PROFILE_NAME" >/dev/null 2>&1; then
  echo "==> creating instance profile $PROFILE_NAME"
  aws iam create-role --role-name "$PROFILE_NAME" \
    --assume-role-policy-document '{
      "Version":"2012-10-17",
      "Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]
    }' >/dev/null
  aws iam attach-role-policy --role-name "$PROFILE_NAME" \
    --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
  # CloudWatch agent needs this to push logs and custom metrics.
  aws iam attach-role-policy --role-name "$PROFILE_NAME" \
    --policy-arn arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy
  aws iam create-instance-profile --instance-profile-name "$PROFILE_NAME" >/dev/null
  aws iam add-role-to-instance-profile \
    --instance-profile-name "$PROFILE_NAME" --role-name "$PROFILE_NAME"
  echo "    waiting for IAM propagation"
  sleep 15
fi

# ── security group with no inbound rules ────────────────────────────────────
VPC_ID="$(aws ec2 describe-vpcs --filters Name=isDefault,Values=true \
  --query 'Vpcs[0].VpcId' --output text)"
SG_ID="$(aws ec2 describe-security-groups \
  --filters "Name=group-name,Values=${NAME}-sg" "Name=vpc-id,Values=$VPC_ID" \
  --query 'SecurityGroups[0].GroupId' --output text 2>/dev/null || echo "None")"

if [[ "$SG_ID" == "None" || -z "$SG_ID" ]]; then
  echo "==> creating security group ${NAME}-sg (egress only)"
  SG_ID="$(aws ec2 create-security-group --group-name "${NAME}-sg" \
    --description "itervox — egress only, access via SSM" \
    --vpc-id "$VPC_ID" --query GroupId --output text)"
fi

# Graviton (arm64) instance families encode a "g" right after the generation
# digit (t4g, m6g, c7g, r6gd, ...); everything else is x86_64. Hard-coding
# x86_64 here breaks any --type Graviton launch since the AMI arch would not
# match the instance arch.
FAMILY="${TYPE%%.*}"
if [[ "$FAMILY" =~ ^[a-z]+[0-9]+g ]]; then
  AMI_ARCH="arm64"
else
  AMI_ARCH="x86_64"
fi

AMI_ID="$(aws ssm get-parameters \
  --names "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-${AMI_ARCH}" \
  --query 'Parameters[0].Value' --output text)"

echo "==> launching $TYPE from $AMI_ID"
INSTANCE_ID="$(aws ec2 run-instances \
  --image-id "$AMI_ID" \
  --instance-type "$TYPE" \
  --iam-instance-profile "Name=$PROFILE_NAME" \
  --security-group-ids "$SG_ID" \
  --block-device-mappings "DeviceName=/dev/xvda,Ebs={VolumeSize=$DISK_GB,VolumeType=gp3}" \
  --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=$NAME}]" \
  --metadata-options "HttpTokens=required" \
  --query 'Instances[0].InstanceId' --output text)"
CREATED_INSTANCE="$INSTANCE_ID"

echo "==> waiting for $INSTANCE_ID to come up"
aws ec2 wait instance-running --instance-ids "$INSTANCE_ID"

# CORE-062: create and attach persistent data disk for state roots.
# An EBS volume can only attach to an instance in the SAME availability zone.
# run-instances gets no placement, so EC2 picks the AZ: read it back instead
# of guessing "${REGION}a" (M4-close BH-M4-6 — the guess failed with
# InvalidVolume.ZoneMismatch whenever EC2 chose another zone).
AZ="$(aws ec2 describe-instances --instance-ids "$INSTANCE_ID" \
  --query 'Reservations[0].Instances[0].Placement.AvailabilityZone' --output text)"
if [[ -z "$AZ" || "$AZ" == "None" ]]; then
  echo "error: could not read the availability zone of $INSTANCE_ID" >&2
  exit 1
fi

echo "==> creating persistent data disk for state roots in $AZ"
VOLUME_ID="$(aws ec2 create-volume \
  --size "$DATA_GB" \
  --volume-type gp3 \
  --availability-zone "$AZ" \
  --tag-specifications "ResourceType=volume,Tags=[{Key=Name,Value=${NAME}-data}]" \
  --query 'VolumeId' --output text)"
CREATED_VOLUME="$VOLUME_ID"

aws ec2 wait volume-available --volume-ids "$VOLUME_ID"

echo "==> attaching data disk to $INSTANCE_ID"
aws ec2 attach-volume \
  --volume-id "$VOLUME_ID" \
  --instance-id "$INSTANCE_ID" \
  --device "/dev/sdf"
aws ec2 wait volume-in-use --volume-ids "$VOLUME_ID"
# No modify-instance-attribute: a volume attached AFTER launch already has
# DeleteOnTermination=false, so the data disk outlives the instance
# (CORE-062). The old call was redundant.

# Success: nothing to clean up from here on.
CREATED_INSTANCE=""
CREATED_VOLUME=""

cat <<EOF

────────────────────────────────────────────────────────────────
 Instance: $INSTANCE_ID

 Bootstrap (SSM, no SSH key, no inbound rule):
   aws ssm start-session --target $INSTANCE_ID
   # then, on the box:
   sudo dnf install -y git && git clone <your-repo-with-deploy-dir> repo \\
     && cd repo && sudo ./deploy/bootstrap.sh --repo <git-url> \\
          --data-disk /dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_${VOLUME_ID//-/}

 Reach the dashboard:
   aws ssm start-session --target $INSTANCE_ID \\
     --document-name AWS-StartPortForwardingSession \\
     --parameters '{"portNumber":["8090"],"localPortNumber":["8090"]}'
   open http://localhost:8090/?token=<ITERVOX_API_TOKEN>

 Logging + alerting: install the CloudWatch agent, then see deploy/monitoring/
   sudo dnf install -y amazon-cloudwatch-agent
────────────────────────────────────────────────────────────────
EOF
