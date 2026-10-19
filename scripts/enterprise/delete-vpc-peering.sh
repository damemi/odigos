#!/usr/bin/env bash
set -euo pipefail

command -v aws >/dev/null || { echo "AWS CLI required"; exit 1; }
command -v jq >/dev/null || { echo "jq required"; exit 1; }

DEFAULT_REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"

echo "Discovering VPC peering connections across AWS regions..."
echo

REGIONS=$(aws ec2 describe-regions \
    --region "$DEFAULT_REGION" \
    --query 'Regions[].RegionName' \
    --output text)

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

echo '[]' > "$TMP"

for REGION in $REGIONS; do
    echo "Checking $REGION..." >&2

    RESULT=$(aws ec2 describe-vpc-peering-connections \
        --region "$REGION" \
        --output json 2>/dev/null) || continue

    echo "$RESULT" | jq --arg region "$REGION" '
        [
            .VpcPeeringConnections[]
            | select(.Status.Code != "deleted")
            | {
                id: .VpcPeeringConnectionId,
                region: $region,
                requester_vpc: .RequesterVpcInfo.VpcId,
                requester_region: (.RequesterVpcInfo.Region // $region),
                accepter_vpc: .AccepterVpcInfo.VpcId,
                accepter_region: (.AccepterVpcInfo.Region // $region),
                status: .Status.Code
            }
        ]
    ' > "${TMP}.new"

    jq -s 'add | unique_by(.id)' \
        "$TMP" "${TMP}.new" > "${TMP}.merged"

    mv "${TMP}.merged" "$TMP"
    rm -f "${TMP}.new"
done

COUNT=$(jq 'length' "$TMP")

if [[ "$COUNT" -eq 0 ]]; then
    echo "No VPC peering connections found."
    exit 0
fi

echo
printf "%-4s %-23s %-16s %-16s %-16s %-16s %-12s\n" \
    "#" "PEERING ID" "REQUESTER VPC" "REQ REGION" \
    "ACCEPTER VPC" "ACC REGION" "STATUS"

echo "------------------------------------------------------------------------------------------------------------------------"

jq -r '
    to_entries[]
    | [
        (.key + 1),
        .value.id,
        .value.requester_vpc,
        .value.requester_region,
        .value.accepter_vpc,
        .value.accepter_region,
        .value.status
    ]
    | @tsv
' "$TMP" | while IFS=$'\t' read -r NUM ID VPC1 REG1 VPC2 REG2 STATUS; do
    printf "%-4s %-23s %-16s %-16s %-16s %-16s %-12s\n" \
        "$NUM" "$ID" "$VPC1" "$REG1" "$VPC2" "$REG2" "$STATUS"
done

echo
read -rp "Select peering to delete (1-$COUNT, or q): " SELECTION

if [[ "$SELECTION" == "q" ]]; then
    exit 0
fi

if ! [[ "$SELECTION" =~ ^[0-9]+$ ]] ||
   (( SELECTION < 1 || SELECTION > COUNT )); then
    echo "Invalid selection"
    exit 1
fi

ENTRY=$(jq -c ".[$((SELECTION - 1))]" "$TMP")

PCX_ID=$(echo "$ENTRY" | jq -r '.id')
REGION=$(echo "$ENTRY" | jq -r '.region')
REGION1=$(echo "$ENTRY" | jq -r '.requester_region')
REGION2=$(echo "$ENTRY" | jq -r '.accepter_region')
VPC1=$(echo "$ENTRY" | jq -r '.requester_vpc')
VPC2=$(echo "$ENTRY" | jq -r '.accepter_vpc')

echo
echo "Selected:"
echo "  Peering: $PCX_ID"
echo "  $VPC1 ($REGION1) <-> $VPC2 ($REGION2)"
echo

read -rp "Type DELETE to confirm: " CONFIRM

if [[ "$CONFIRM" != "DELETE" ]]; then
    echo "Cancelled."
    exit 0
fi

remove_routes() {
    local REGION="$1"
    local VPC="$2"

    echo "Removing peering routes in $VPC ($REGION)..."

    ROUTE_TABLES=$(aws ec2 describe-route-tables \
        --region "$REGION" \
        --filters "Name=vpc-id,Values=$VPC" \
        --output json 2>/dev/null) || {
            echo "WARNING: Cannot access route tables in $REGION"
            return
        }

    echo "$ROUTE_TABLES" | jq -r \
        --arg pcx "$PCX_ID" '
        .RouteTables[]
        | .RouteTableId as $rtb
        | .Routes[]
        | select(.VpcPeeringConnectionId == $pcx)
        | [
            $rtb,
            (.DestinationCidrBlock //
             .DestinationIpv6CidrBlock //
             .DestinationPrefixListId)
        ]
        | @tsv
    ' | while IFS=$'\t' read -r RTB CIDR; do

        echo "  Removing $RTB -> $CIDR"

        if [[ "$CIDR" == *:* ]]; then
            DEST_ARG=(--destination-ipv6-cidr-block "$CIDR")
        elif [[ "$CIDR" == pl-* ]]; then
            DEST_ARG=(--destination-prefix-list-id "$CIDR")
        else
            DEST_ARG=(--destination-cidr-block "$CIDR")
        fi

        aws ec2 delete-route \
            --region "$REGION" \
            --route-table-id "$RTB" \
            "${DEST_ARG[@]}" \
            >/dev/null || echo "WARNING: Failed to remove route"
    done
}

remove_routes "$REGION1" "$VPC1"
remove_routes "$REGION2" "$VPC2"

echo "Deleting VPC peering connection..."

aws ec2 delete-vpc-peering-connection \
    --region "$REGION" \
    --vpc-peering-connection-id "$PCX_ID" \
    >/dev/null

echo
echo "=================================="
echo "VPC Peering deleted!"
echo "Peering ID: $PCX_ID"
echo "=================================="
