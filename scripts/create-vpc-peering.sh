#!/usr/bin/env bash
set -euo pipefail

# Peers a workload VPC with the VPC hosting the region's managed Odigos Collector, and opens the path
# the connector proves before it routes any workload to that Collector
# (connectors/providers/aws/pkg/collector/placement/topology.go):
#
#   - Both VPCs in the SAME region. A cross-region peering is never treated as reachable, so workloads
#     behind one keep exporting straight to the destination.
#   - An active direct peering between them, with no overlapping CIDRs.
#   - In EVERY route table of each VPC, a route via that peering to every associated CIDR of the other.
#     ECS may place a task in any subnet, so one routed subnet does not prove the path.
#   - The Collector security group admitting the workloads on TCP 4317 (OTLP gRPC) and 4318 (OTLP
#     HTTP), and itself on 4317/4318 (NLB forwarding) and 13133 (NLB health checks).
#     13134 is never exposed.
#
# Collector security groups: one tagged odigos-managed=true is converged by the connector, which adds
# the workloads' groups itself and revokes any rule added by hand, so it is reported and left alone. A
# customer-owned one (manual mode) gets the rules here: from --workload-sg if given, otherwise from the
# workload VPC's CIDRs. Without --collector-sg, the groups tagged odigos-component=collector in the
# collector VPC are used.
#
# Usage:
#   ./create-vpc-peering.sh <collector-vpc-id> <workload-vpc-id> [region] [options]
#
# Options:
#   --collector-sg <sg-id>  customer-owned Collector security group (repeatable)
#   --workload-sg <sg-id>   security group the workloads send from (repeatable). Admitted by reference
#                           on the Collector group, and given egress to the Collector if its own
#                           egress is restricted.
#
# Example:
#   ./create-vpc-peering.sh vpc-aaa vpc-bbb us-east-2 --workload-sg sg-0123
#
# Peering-specific SG rules are marked for cleanup by delete-vpc-peering.sh.
# Shared NLB/self-health rules survive deletion because other VPCs may use them.
# If the region is omitted, uses AWS_REGION / AWS_DEFAULT_REGION. Safe to re-run.

USAGE="Usage: $0 <collector-vpc-id> <workload-vpc-id> [region] [--collector-sg <sg-id>]... [--workload-sg <sg-id>]..."

POSITIONAL=()
COLLECTOR_SGS=()
WORKLOAD_SGS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --collector-sg) COLLECTOR_SGS+=("${2:?$USAGE}"); shift 2 ;;
        --workload-sg) WORKLOAD_SGS+=("${2:?$USAGE}"); shift 2 ;;
        -h|--help) echo "$USAGE"; exit 0 ;;
        -*) echo "ERROR: Unknown option $1"; echo "$USAGE"; exit 1 ;;
        *) POSITIONAL+=("$1"); shift ;;
    esac
done

if [[ ${#POSITIONAL[@]} -gt 3 ]]; then
    echo "ERROR: The collector and workload VPCs must be in the same region."
    echo "Odigos never routes workloads to a Collector over a cross-region peering."
    exit 1
fi
if [[ ${#POSITIONAL[@]} -lt 2 ]]; then
    echo "$USAGE"
    exit 1
fi

COLLECTOR_VPC="${POSITIONAL[0]}"
WORKLOAD_VPC="${POSITIONAL[1]}"
REGION="${POSITIONAL[2]:-${AWS_REGION:-${AWS_DEFAULT_REGION:-}}}"

if [[ -z "$REGION" ]]; then
    echo "ERROR: Specify a region or set AWS_REGION."
    exit 1
fi
if [[ "$COLLECTOR_VPC" == "$WORKLOAD_VPC" ]]; then
    echo "ERROR: The collector and workload VPCs are the same; nothing to peer."
    exit 1
fi

command -v aws >/dev/null || { echo "AWS CLI required"; exit 1; }
command -v python3 >/dev/null || { echo "Python 3 required"; exit 1; }

# Steps that could not be completed. Reported at the end rather than aborting, so one conflicting
# route does not leave the rest of the path unconfigured.
FAILURES=0

echo "Fetching VPC information..."

# Only associated blocks, matching what the connector's discovery counts: a block that is still
# associating or already disassociated is neither routed nor proven.
vpc_cidrs() {
    aws ec2 describe-vpcs \
        --region "$REGION" \
        --vpc-ids "$1" \
        --query "Vpcs[0].CidrBlockAssociationSet[?CidrBlockState.State=='associated'].CidrBlock" \
        --output text
}

COLLECTOR_CIDRS=$(vpc_cidrs "$COLLECTOR_VPC") ||
    { echo "ERROR: $COLLECTOR_VPC not found in $REGION"; exit 1; }
WORKLOAD_CIDRS=$(vpc_cidrs "$WORKLOAD_VPC") ||
    { echo "ERROR: $WORKLOAD_VPC not found in $REGION (both VPCs must be in the same region)"; exit 1; }

if [[ -z "$COLLECTOR_CIDRS" || -z "$WORKLOAD_CIDRS" ]]; then
    echo "ERROR: Could not read the associated CIDR blocks of both VPCs."
    exit 1
fi

export COLLECTOR_CIDRS WORKLOAD_CIDRS

python3 - <<'PY'
import ipaddress
import os
import sys

a = [ipaddress.ip_network(x) for x in os.environ["COLLECTOR_CIDRS"].split()]
b = [ipaddress.ip_network(x) for x in os.environ["WORKLOAD_CIDRS"].split()]

for x in a:
    for y in b:
        if x.overlaps(y):
            print(f"ERROR: Overlapping CIDRs: {x} and {y}")
            sys.exit(1)
PY

# AWS allows only one live peering between the same two VPCs, so an existing one is reused rather
# than failing the create.
PCX_ID=$(aws ec2 describe-vpc-peering-connections \
    --region "$REGION" \
    --filters "Name=requester-vpc-info.vpc-id,Values=$COLLECTOR_VPC,$WORKLOAD_VPC" \
              "Name=accepter-vpc-info.vpc-id,Values=$COLLECTOR_VPC,$WORKLOAD_VPC" \
              "Name=status-code,Values=initiating-request,pending-acceptance,provisioning,active" \
    --query 'VpcPeeringConnections[0].VpcPeeringConnectionId' \
    --output text)

if [[ "$PCX_ID" == "None" ]]; then
    echo "Creating VPC peering..."

    PCX_ID=$(aws ec2 create-vpc-peering-connection \
        --region "$REGION" \
        --vpc-id "$COLLECTOR_VPC" \
        --peer-vpc-id "$WORKLOAD_VPC" \
        --peer-region "$REGION" \
        --query 'VpcPeeringConnection.VpcPeeringConnectionId' \
        --output text)

    echo "Created: $PCX_ID"

    aws ec2 wait vpc-peering-connection-exists \
        --region "$REGION" \
        --vpc-peering-connection-ids "$PCX_ID"
else
    echo "Reusing existing peering: $PCX_ID"
fi

echo "Waiting for active status..."

STATUS=""
ACCEPTED=0
for _ in {1..30}; do
    STATUS=$(aws ec2 describe-vpc-peering-connections \
        --region "$REGION" \
        --vpc-peering-connection-ids "$PCX_ID" \
        --query 'VpcPeeringConnections[0].Status.Code' \
        --output text)

    case "$STATUS" in
        active|failed|rejected|expired|deleting|deleted) break ;;
        pending-acceptance)
            if [[ "$ACCEPTED" -eq 0 ]]; then
                echo "Accepting peering..."
                aws ec2 accept-vpc-peering-connection \
                    --region "$REGION" \
                    --vpc-peering-connection-id "$PCX_ID" >/dev/null
                ACCEPTED=1
            fi
            ;;
    esac
    sleep 2
done

if [[ "$STATUS" != "active" ]]; then
    echo "ERROR: Peering did not become active (status: $STATUS)"
    exit 1
fi

# ensure_route makes RTB send CIDR through the peering. The connector only counts a route whose
# target IS this peering, so an existing route to the same destination through anything else is a
# conflict: reported, never overwritten, unless it is blackholed (its target is gone).
ensure_route() {
    local RTB="$1"
    local CIDR="$2"
    local STATE PCX

    read -r STATE PCX <<<"$(aws ec2 describe-route-tables \
        --region "$REGION" \
        --route-table-ids "$RTB" \
        --query "RouteTables[0].Routes[?DestinationCidrBlock=='$CIDR'] | [0].[State, VpcPeeringConnectionId]" \
        --output text)"
    [[ "$PCX" == "None" ]] && PCX=""

    if [[ "$STATE" == "None" ]]; then
        aws ec2 create-route \
            --region "$REGION" \
            --route-table-id "$RTB" \
            --destination-cidr-block "$CIDR" \
            --vpc-peering-connection-id "$PCX_ID" \
            >/dev/null
        echo "  $RTB -> $CIDR: added"
    elif [[ "$PCX" == "$PCX_ID" ]]; then
        echo "  $RTB -> $CIDR: present"
    elif [[ "$STATE" == "blackhole" ]]; then
        aws ec2 replace-route \
            --region "$REGION" \
            --route-table-id "$RTB" \
            --destination-cidr-block "$CIDR" \
            --vpc-peering-connection-id "$PCX_ID" \
            >/dev/null
        echo "  $RTB -> $CIDR: replaced blackholed route"
    else
        echo "  $RTB -> $CIDR: ERROR: already routed elsewhere${PCX:+ ($PCX)}; it must go via $PCX_ID"
        FAILURES=$((FAILURES + 1))
    fi
}

# Every table, not only the ones associated with a subnet: the connector checks each subnet's
# effective table, which for an unassociated subnet is the VPC's main table.
ensure_routes() {
    local VPC="$1"
    local REMOTE_CIDRS="$2"
    local RTB CIDR

    echo "Configuring routes for $VPC..."

    for RTB in $(aws ec2 describe-route-tables \
        --region "$REGION" \
        --filters "Name=vpc-id,Values=$VPC" \
        --query 'RouteTables[].RouteTableId' \
        --output text); do
        for CIDR in $REMOTE_CIDRS; do
            ensure_route "$RTB" "$CIDR"
        done
    done
}

ensure_routes "$COLLECTOR_VPC" "$WORKLOAD_CIDRS"
ensure_routes "$WORKLOAD_VPC" "$COLLECTOR_CIDRS"

# authorize <ingress|egress> <group-id> <ip-permissions-json> <label>
authorize() {
    local ERR

    if ERR=$(aws ec2 "authorize-security-group-$1" \
        --region "$REGION" \
        --group-id "$2" \
        --ip-permissions "$3" 2>&1 >/dev/null); then
        echo "  $4: added"
    elif [[ "$ERR" == *InvalidPermission.Duplicate* ]]; then
        echo "  $4: present"
    else
        echo "  $4: ERROR: $ERR"
        FAILURES=$((FAILURES + 1))
    fi
}

group_vpc() {
    aws ec2 describe-security-groups \
        --region "$REGION" \
        --group-ids "$1" \
        --query 'SecurityGroups[0].VpcId' \
        --output text
}

# Set when the Collector's ingress is Odigos's to converge, which changes the closing advice.
ODIGOS_MANAGED=0

ensure_collector_sg() {
    local SG="$1"
    local PORT SRC CIDR MANAGED

    if [[ "$(group_vpc "$SG")" != "$COLLECTOR_VPC" ]]; then
        echo "  $SG: ERROR: not in the collector VPC $COLLECTOR_VPC"
        FAILURES=$((FAILURES + 1))
        return
    fi

    MANAGED=$(aws ec2 describe-security-groups \
        --region "$REGION" \
        --group-ids "$SG" \
        --query "SecurityGroups[0].Tags[?Key=='odigos-managed'] | [0].Value" \
        --output text)

    if [[ "$MANAGED" == "true" ]]; then
        echo "  $SG: Odigos-managed; the connector converges its rules and revokes manual ones, skipping"
        echo "    It must admit workload -> NLB and NLB -> Collector on 4317/4318."
        ODIGOS_MANAGED=1
        return
    fi

    for PORT in 4317 4318; do
        if [[ ${#WORKLOAD_SGS[@]} -gt 0 ]]; then
            for SRC in "${WORKLOAD_SGS[@]}"; do
                authorize ingress "$SG" \
                    "[{\"IpProtocol\":\"tcp\",\"FromPort\":$PORT,\"ToPort\":$PORT,\"UserIdGroupPairs\":[{\"GroupId\":\"$SRC\",\"Description\":\"odigos-vpc-peering:$PCX_ID\"}]}]" \
                    "$SG <- $SRC tcp/$PORT"
            done
        else
            for CIDR in $WORKLOAD_CIDRS; do
                authorize ingress "$SG" \
                    "[{\"IpProtocol\":\"tcp\",\"FromPort\":$PORT,\"ToPort\":$PORT,\"IpRanges\":[{\"CidrIp\":\"$CIDR\",\"Description\":\"odigos-vpc-peering:$PCX_ID\"}]}]" \
                    "$SG <- $CIDR tcp/$PORT"
            done
        fi
    done

    # Shared backend permissions belong to the Collector, not this peering.
    for PORT in 4317 4318; do
        authorize ingress "$SG" \
            "[{\"IpProtocol\":\"tcp\",\"FromPort\":$PORT,\"ToPort\":$PORT,\"UserIdGroupPairs\":[{\"GroupId\":\"$SG\",\"Description\":\"Odigos NLB to Collector telemetry\"}]}]" \
            "$SG <- self tcp/$PORT (NLB forwarding)"
    done

    authorize ingress "$SG" \
        "[{\"IpProtocol\":\"tcp\",\"FromPort\":13133,\"ToPort\":13133,\"UserIdGroupPairs\":[{\"GroupId\":\"$SG\"}]}]" \
        "$SG <- self tcp/13133 (NLB health check)"
}

echo "Configuring Collector security groups..."

if [[ ${#COLLECTOR_SGS[@]} -eq 0 ]]; then
    read -ra COLLECTOR_SGS <<<"$(aws ec2 describe-security-groups \
        --region "$REGION" \
        --filters "Name=vpc-id,Values=$COLLECTOR_VPC" "Name=tag:odigos-component,Values=collector" \
        --query 'SecurityGroups[].GroupId' \
        --output text)"
fi

if [[ ${#COLLECTOR_SGS[@]} -eq 0 ]]; then
    echo "  None tagged odigos-component=collector in $COLLECTOR_VPC."
    echo "  A managed Collector creates and converges its own; pass --collector-sg for a hand-built one."
    ODIGOS_MANAGED=1
else
    for SG in "${COLLECTOR_SGS[@]}"; do
        ensure_collector_sg "$SG"
    done
fi

# A workload group that already allows all egress needs nothing; a restricted one is opened to the
# Collector's data ports only.
ensure_workload_egress() {
    local SG="$1"
    local PORT CIDR OPEN

    if [[ "$(group_vpc "$SG")" != "$WORKLOAD_VPC" ]]; then
        echo "  $SG: ERROR: not in the workload VPC $WORKLOAD_VPC"
        FAILURES=$((FAILURES + 1))
        return
    fi

    OPEN=$(aws ec2 describe-security-groups \
        --region "$REGION" \
        --group-ids "$SG" \
        --query "length(SecurityGroups[0].IpPermissionsEgress[?IpProtocol=='-1' && contains(IpRanges[].CidrIp, '0.0.0.0/0')])" \
        --output text)

    if [[ "$OPEN" != "0" ]]; then
        echo "  $SG: all egress already allowed"
        return
    fi

    for PORT in 4317 4318; do
        for CIDR in $COLLECTOR_CIDRS; do
            authorize egress "$SG" \
                "[{\"IpProtocol\":\"tcp\",\"FromPort\":$PORT,\"ToPort\":$PORT,\"IpRanges\":[{\"CidrIp\":\"$CIDR\",\"Description\":\"odigos-vpc-peering:$PCX_ID\"}]}]" \
                "$SG -> $CIDR tcp/$PORT"
        done
    done
}

if [[ ${#WORKLOAD_SGS[@]} -gt 0 ]]; then
    echo "Configuring workload security group egress..."
    for SG in "${WORKLOAD_SGS[@]}"; do
        ensure_workload_egress "$SG"
    done
fi

echo
echo "=================================="
echo "Peering ID:    $PCX_ID ($REGION)"
echo "Collector VPC: $COLLECTOR_VPC ($(echo $COLLECTOR_CIDRS))"
echo "Workload VPC:  $WORKLOAD_VPC ($(echo $WORKLOAD_CIDRS))"
echo "=================================="

if [[ "$ODIGOS_MANAGED" -eq 1 ]]; then
    echo
    echo "Odigos must reconcile workload ingress and NLB-to-Collector ingress on 4317/4318."
    echo "If targets are healthy but OTLP times out, check that the Collector SG admits itself on both ports."
    echo "An older connector missing those rules needs an update; manual additions will be revoked."
    echo "If workload security groups do not resolve, its"
    echo "VPC-CIDR fallback admits only the collector VPC's blocks, never a peered range; set"
    echo "  collector.regions.$REGION.ingress.additionalCidrs: [$(echo $WORKLOAD_CIDRS | sed 's/ /, /g')]"
fi

if [[ "$FAILURES" -gt 0 ]]; then
    echo
    echo "ERROR: $FAILURES step(s) above need attention before the Collector is reachable."
    exit 1
fi

echo
echo "VPC peering and routes are ready. Verify Collector ingress separately."
