#!/usr/bin/env bash
#
# Provision an AWS EC2 instance for itervox.
#
# Defaults to NO inbound security-group rule at all. Access is via SSM Session
# Manager port forwarding, gated by IAM — stronger than a bearer token, and it
# avoids the loopback-behind-proxy auth gap (issue #48).
#
# Requires an instance profile with AmazonSSMManagedInstanceCore attached;
# the script creates one if absent.
#
# Usage: ./provision.sh [--name itervox] [--region us-east-1]
#
set -euo pipefail

NAME="itervox"
REGION="${AWS_REGION:-us-east-1}"
TYPE="t3.xlarge"
DISK_GB=100
PROFILE_NAME="ItervoxSSMInstanceProfile"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)   NAME="$2"; shift 2 ;;
    --region) REGION="$2"; shift 2 ;;
    --type)   TYPE="$2"; shift 2 ;;
    --disk)   DISK_GB="$2"; shift 2 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

export AWS_REGION="$REGION"

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

AMI_ID="$(aws ssm get-parameters \
  --names /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64 \
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

echo "==> waiting for $INSTANCE_ID to come up"
aws ec2 wait instance-running --instance-ids "$INSTANCE_ID"

cat <<EOF

────────────────────────────────────────────────────────────────
 Instance: $INSTANCE_ID

 Bootstrap (SSM, no SSH key, no inbound rule):
   aws ssm start-session --target $INSTANCE_ID
   # then, on the box:
   sudo dnf install -y git && git clone <your-repo-with-deploy-dir> \\
     && sudo ./deploy/bootstrap.sh --repo <git-url>

 Reach the dashboard:
   aws ssm start-session --target $INSTANCE_ID \\
     --document-name AWS-StartPortForwardingSession \\
     --parameters '{"portNumber":["8090"],"localPortNumber":["8090"]}'
   open http://localhost:8090/?token=<ITERVOX_API_TOKEN>

 Logging + alerting: install the CloudWatch agent, then see deploy/monitoring/
   sudo dnf install -y amazon-cloudwatch-agent
────────────────────────────────────────────────────────────────
EOF
