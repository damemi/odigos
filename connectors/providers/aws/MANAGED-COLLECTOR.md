# AWS managed Collector setup

The installation steps shown in Central come from [`connectors/spec/data/aws.yaml`](../../spec/data/aws.yaml). That spec is the source for the overview, AWS CLI, and CloudFormation variants. Manual mode contains the complete customer deployment flow. Automatic mode contains the prerequisites; the connector then creates and maintains the Collector infrastructure.

## IAM identities and mode permissions

An operator can run the AWS CLI or deploy CloudFormation with any AWS identity authorized to provision the required resources. That identity is not part of the Collector configuration and does not need to be trusted by the connector.

The connector uses **its existing AWS authentication** for Collector operations, exactly as it does for workload discovery and instrumentation. No separate Collector observation or deployment role is required. Its permissions depend on the selected Collector mode:

| Mode | Connector permissions | AWS infrastructure actions |
| --- | --- | --- |
| Off | No Collector permissions | None |
| Manual | Collector management permissions from `aws.yaml` | Read ECS, ELB, and VPC state; discover and report tagged regional Collectors; push configuration over OpAMP. The connector does not change Collector infrastructure. |
| Automatic | Collector management and deployment permissions from `aws.yaml` | Read, create, converge, and tear down connector-owned Collector resources. |

The **ECS task execution role** is still required in both manual and automatic mode. ECS tasks use it to pull the Collector image, read the Collector secrets, and write logs. The setup instructions create this role with an `ecs-tasks.amazonaws.com` trust policy and a policy scoped to the connector's Collector secrets and log group. Supply its ARN as `providerOverrides.aws.executionRoleArn`. You can use an equivalent existing role with the same access.

The legacy GraphQL field `deploymentRoleRef` may still appear in older clients and reports. It is ignored by the AWS Collector runtime and is not an installation requirement. Until all generated GraphQL clients use the optional schema, older clients may send an empty string for compatibility.

## Prerequisites shared by both modes

Choose an AWS region, an existing ECS cluster that can run Fargate tasks, and a VPC with subnets that can reach the image registry, Secrets Manager, the Odigos OpAMP endpoint, and telemetry destinations. The Collector's network load balancer is internal. Workload VPCs need network reachability to the Collector VPC, including reciprocal routes and suitable security group rules. Choose task public IP assignment according to the subnets' egress path.

Create or select the ECS task execution role using one of the generated setup variants. Automatic mode stops its installation instructions after these prerequisites; the connector provisions the remaining infrastructure when the Collector is created.

## Manual installation

Select **Manual** and follow one complete variant in the Collector setup wizard:

1. **Overview** explains the resources and the values the operator must supply.
2. **AWS CLI** creates the execution role and customer-owned Collector resources with shell commands.
3. **CloudFormation** creates the execution role and customer-owned Collector resources with templates and deployment commands.

The CLI and CloudFormation variants lead to the same deployed layout. The operator can use any appropriate provisioning credentials. The Connector IAM identity needs only the manual mode read permissions shown during connector setup. The steps in `aws.yaml` are rendered with the selected region, account, connector, cluster, VPC, and execution role values; the generated commands and templates are the installation artifacts to apply.

Manual infrastructure must include the Odigos Collector identity tags from the generated instructions. The connector scans enabled regions and ECS clusters for an active service named `odigos-collector-<region>` with matching connector tags. It discovers that service and records its health and endpoint even before that region has been saved in the UI. An undeclared discovery is observed only; it is not used to route workloads until a host network is configured. If multiple matching services exist in one region, configure the intended cluster explicitly.

After applying the resources, review the Collector report for the image, ECS task, target group, OpAMP, and destination state. The connector observes customer-owned infrastructure but never repairs or deletes it in manual mode. Apply any infrastructure changes through your chosen AWS provisioning process.

## Automatic installation

Select **Automatic**, complete the shared prerequisites, and provide the ECS cluster, Collector VPC, and task execution role ARN. The connector uses its existing AWS credentials with the deployment permissions from `aws.yaml` to create and maintain its tagged ECS service, internal load balancer, security group, log group, secrets, and scaling resources. It does not create or modify the chosen VPC, subnets, ECS cluster, or task execution role.

## 3. Find or create an ECS cluster and choose the VPC

The connector uses an existing ECS cluster and does not create or modify it. Select an active cluster that supports Fargate, or create an empty cluster if the account has none. The tasks use `awsvpc` networking. Pick the VPC that should host the Collector. A VPC is the network boundary; its subnets are address ranges in individual Availability Zones. Once you provide `networkId` for that VPC, Odigos can choose the subnets inside it. For the standard path, leave `providerOverrides.aws.subnetIds` out of the create input; AWS does not choose them on the service's behalf.

The Collector's network load balancer (NLB) is internal and provides an endpoint to workloads that can reach this VPC. The Collector tasks separately need outbound access to AWS Secrets Manager, the image registry, Odigos OpAMP, and telemetry destinations. Private subnets normally get this through a NAT gateway or another working egress path. Subnets with a default route to an internet gateway need `assignPublicIp: true` for Fargate tasks to use that route; setting it to `false` there causes task startup to time out. A subnet's `MapPublicIpOnLaunch` setting does not give a Fargate task a public IP.

```sh
aws ecs list-clusters --region "$AWS_REGION" --query 'clusterArns[]' --output table
printf 'Paste the ECS cluster ARN or name to use (or enter a new name): '
read -r ECS_CLUSTER_REF
export ECS_CLUSTER_ARN=$(aws ecs describe-clusters --region "$AWS_REGION" --clusters "$ECS_CLUSTER_REF" \
  --query 'clusters[?status==`ACTIVE`].clusterArn | [0]' --output text)
if [ -n "$ECS_CLUSTER_ARN" ] && [ "$ECS_CLUSTER_ARN" != "None" ]; then
  printf 'Reusing active cluster %s\n' "$ECS_CLUSTER_ARN"
else
  export ECS_CLUSTER_NAME=$ECS_CLUSTER_REF
  aws ecs create-cluster --region "$AWS_REGION" --cluster-name "$ECS_CLUSTER_NAME" \
    --capacity-providers FARGATE --default-capacity-provider-strategy capacityProvider=FARGATE,weight=1 |
    jq -e '.cluster | {clusterArn, status, capacityProviders}'
  export ECS_CLUSTER_ARN=$(aws ecs describe-clusters --region "$AWS_REGION" --clusters "$ECS_CLUSTER_NAME" \
    --query 'clusters[0].clusterArn' --output text)
fi
printf 'Using ECS cluster: %s\n' "$ECS_CLUSTER_ARN"

aws ec2 describe-vpcs --region "$AWS_REGION" \
  --query 'Vpcs[].{VpcId:VpcId,Cidr:CidrBlock,Default:IsDefault}' --output table
printf 'Paste the VPC ID for the Collector: '
read -r COLLECTOR_VPC_ID
if [ -z "$COLLECTOR_VPC_ID" ]; then
  printf 'A VPC ID is required.\n' >&2
  exit 1
fi
export COLLECTOR_VPC_ID
if ! aws ec2 describe-vpcs --region "$AWS_REGION" --vpc-ids "$COLLECTOR_VPC_ID" \
  --query 'Vpcs[0].{VpcId:VpcId,Cidr:CidrBlock}' --output table; then
  printf 'The VPC ID must exist in %s.\n' "$AWS_REGION" >&2
  exit 1
fi
aws ec2 describe-subnets --region "$AWS_REGION" \
  --filters "Name=vpc-id,Values=$COLLECTOR_VPC_ID" \
  --query 'Subnets[].{SubnetId:SubnetId,AZ:AvailabilityZone,Cidr:CidrBlock,FreeIPs:AvailableIpAddressCount}' --output table
```

Use the subnet table to confirm the VPC has free addresses in the Availability Zones you want. The CIDR and Availability Zone columns do **not** tell you whether a subnet is public. Check each candidate subnet's associated route table in the AWS VPC console. A default route to a NAT gateway or central egress is a private outbound path. A default route to an internet gateway is a public outbound path that Fargate can use only when its task has a public IP. If neither route is present, add an egress path before deploying.

Set the public-IP option once here, based on that route check. The create command in step 7 uses this value without resetting it:

```sh
printf 'Collector outbound path in this VPC [private/public]: '
read -r COLLECTOR_EGRESS
case "$COLLECTOR_EGRESS" in
  private) export ASSIGN_PUBLIC_IP=false ;;
  public)  export ASSIGN_PUBLIC_IP=true ;;
  *) printf 'Enter private (NAT or central egress) or public (internet gateway).\n' >&2; exit 1 ;;
esac
printf 'Fargate task public IPs: %s\n' "$ASSIGN_PUBLIC_IP"
```

If you select `public`, the Fargate tasks get public IPs for outbound traffic; the Collector's NLB and OTLP endpoint remain internal. If you select `private`, the tasks receive no public IPs and need a working private egress path. For the default VPC whose subnets route only through an internet gateway, select `public`; selecting `private` there reproduces the Secrets Manager timeout shown earlier.

With `subnetIds` omitted, Odigos discovers subnets in `COLLECTOR_VPC_ID` and selects those with free addresses and a usable route. It prefers private-egress subnets, chooses at most one subnet per Availability Zone, and spreads across up to three zones. If it finds no private-egress subnet, it can select internet-gateway subnets only when `ASSIGN_PUBLIC_IP=true`. After deployment, `details.subnetIds` in the report shows the actual selected subnets. `desired.providerOverrides.subnetIds` remains empty because you did not pin them. A single-zone selection is allowed but reported as reduced availability.

Only set `subnetIds` explicitly when you need fixed placement—for example, to restrict the Collector to approved subnets. Every named subnet must belong to `COLLECTOR_VPC_ID` and have free addresses. Explicit IDs are treated as your placement choice; Odigos reports route concerns but does not silently replace your choices. Existing route tables/security groups are observed for reachability; automatic deployment creates a dedicated Collector security group. The VPC/subnet connectivity itself remains customer-owned.

## 4. Create the ECS task execution role

This role belongs to ECS tasks. Its permissions are restricted to this connector's Collector logs and secrets. The commands create or reuse the dedicated CloudFormation stack below. If you already have an equivalent task execution role outside CloudFormation, skip the stack commands and set `TASK_EXECUTION_ROLE_ARN` to its ARN after verifying the trust policy and permissions. Save the following template in your current directory:

```sh
export EXECUTION_ROLE_TEMPLATE="${TMPDIR:-/tmp}/odigos-collector-execution-role-${CONNECTOR_NAME}.yaml"
cat > "$EXECUTION_ROLE_TEMPLATE" <<'YAML'
AWSTemplateFormatVersion: "2010-09-09"
Description: ECS task execution role for one Odigos Collector connector
Parameters:
  ConnectorName:
    Type: String
    MinLength: 1
Resources:
  CollectorExecutionRole:
    Type: AWS::IAM::Role
    Properties:
      AssumeRolePolicyDocument:
        Version: "2012-10-17"
        Statement:
          - Effect: Allow
            Principal: { Service: ecs-tasks.amazonaws.com }
            Action: sts:AssumeRole
            Condition:
              StringEquals: { aws:SourceAccount: !Ref 'AWS::AccountId' }
              ArnLike: { aws:SourceArn: !Sub 'arn:${AWS::Partition}:ecs:*:${AWS::AccountId}:*' }
      Policies:
        - PolicyName: OdigosCollectorExecution
          PolicyDocument:
            Version: "2012-10-17"
            Statement:
              - Effect: Allow
                Action: [logs:CreateLogStream, logs:PutLogEvents]
                Resource: !Sub 'arn:${AWS::Partition}:logs:*:${AWS::AccountId}:log-group:/odigos/collector/${ConnectorName}:*'
              - Effect: Allow
                Action: secretsmanager:GetSecretValue
                Resource: !Sub 'arn:${AWS::Partition}:secretsmanager:*:${AWS::AccountId}:secret:odigos/collector/${ConnectorName}/*'
Outputs:
  ExecutionRoleArn:
    Value: !GetAtt CollectorExecutionRole.Arn
YAML

export EXECUTION_ROLE_STACK=odigos-collector-execution-role
if aws cloudformation describe-stacks --region "$AWS_REGION" --stack-name "$EXECUTION_ROLE_STACK" >/dev/null 2>&1; then
  printf 'Reusing CloudFormation stack %s\n' "$EXECUTION_ROLE_STACK"
else
  aws cloudformation deploy --region "$AWS_REGION" \
    --stack-name "$EXECUTION_ROLE_STACK" \
    --template-file "$EXECUTION_ROLE_TEMPLATE" \
    --parameter-overrides "ConnectorName=$CONNECTOR_NAME" \
    --capabilities CAPABILITY_IAM
fi
export TASK_EXECUTION_ROLE_ARN=$(aws cloudformation describe-stacks --region "$AWS_REGION" \
  --stack-name "$EXECUTION_ROLE_STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='ExecutionRoleArn'].OutputValue | [0]" --output text)
printf 'ECS task execution role: %s\n' "$TASK_EXECUTION_ROLE_ARN"
```

If reusing an existing stack, verify its `ConnectorName` parameter and output role before continuing. If the stack name belongs to something else, use a new stack name consistently. The execution role ARN is passed as `providerOverrides.aws.executionRoleArn`; it is different from `deploymentRoleRef`.

## 5. Create the customer-owned Collector deployment role

The trust policy names the **exact** IAM user from step 2. This role is separate from the connector's IAM user and the ECS task execution role. The policy below grants the automatic-mode Collector actions to this role only. `iam:PassRole` is limited to the task execution role and ECS tasks. If a dedicated role with this name already exists, inspect its trust policy first; do not replace trust used by other systems. Add the connector principal to its trust policy through your normal IAM/IaC change process, or choose another role name.

```sh
export COLLECTOR_DEPLOYMENT_ROLE_NAME=OdigosCollectorDeploymentRole
jq -n --arg principal "$CONNECTOR_PRINCIPAL_ARN" '{
  Version:"2012-10-17",
  Statement:[{Effect:"Allow",Principal:{AWS:$principal},Action:"sts:AssumeRole"}]
}' > odigos-collector-deployment-trust.json

if aws iam get-role --role-name "$COLLECTOR_DEPLOYMENT_ROLE_NAME" >/dev/null 2>&1; then
  printf 'Role already exists; verify its trust policy before attaching the dedicated policy.\n'
  aws iam get-role --role-name "$COLLECTOR_DEPLOYMENT_ROLE_NAME" --query 'Role.AssumeRolePolicyDocument' --output json
else
  aws iam create-role --role-name "$COLLECTOR_DEPLOYMENT_ROLE_NAME" \
    --assume-role-policy-document file://odigos-collector-deployment-trust.json
fi
```

Create the role's infrastructure policy. `jq` inserts the exact task role ARN as JSON, preventing malformed ARN quoting or escaped shell variables.

```sh
jq -n --arg executionRoleArn "$TASK_EXECUTION_ROLE_ARN" --arg region "$AWS_REGION" '{
  Version:"2012-10-17",
  Statement:[
    {Sid:"CollectorInfrastructure",Effect:"Allow",Resource:"*",
     Condition:{StringEquals:{"aws:RequestedRegion":$region}},Action:[
      "ec2:DescribeVpcs","ec2:DescribeSubnets","ec2:DescribeSecurityGroups",
      "ec2:DescribeRouteTables","ec2:DescribeVpcPeeringConnections",
      "ec2:CreateSecurityGroup","ec2:DeleteSecurityGroup",
      "ec2:AuthorizeSecurityGroupIngress","ec2:RevokeSecurityGroupIngress",
      "ec2:AuthorizeSecurityGroupEgress","ec2:CreateTags",
      "ecs:DescribeClusters","ecs:DescribeServices","ecs:ListTasks","ecs:DescribeTasks",
      "ecs:DescribeTaskDefinition","ecs:RegisterTaskDefinition","ecs:CreateService",
      "ecs:UpdateService","ecs:DeleteService","ecs:StopTask","ecs:TagResource",
      "elasticloadbalancing:DescribeLoadBalancers","elasticloadbalancing:DescribeListeners",
      "elasticloadbalancing:DescribeTargetGroups","elasticloadbalancing:DescribeTargetHealth",
      "elasticloadbalancing:DescribeTags","elasticloadbalancing:CreateLoadBalancer",
      "elasticloadbalancing:DeleteLoadBalancer","elasticloadbalancing:CreateTargetGroup",
      "elasticloadbalancing:DeleteTargetGroup","elasticloadbalancing:ModifyTargetGroupAttributes",
      "elasticloadbalancing:CreateListener","elasticloadbalancing:ModifyListener",
      "elasticloadbalancing:SetSecurityGroups","elasticloadbalancing:AddTags",
      "logs:CreateLogGroup","logs:DeleteLogGroup","logs:PutRetentionPolicy",
      "logs:TagResource","logs:ListTagsForResource",
      "secretsmanager:CreateSecret","secretsmanager:PutSecretValue",
      "secretsmanager:DescribeSecret","secretsmanager:DeleteSecret",
      "secretsmanager:TagResource","secretsmanager:RestoreSecret",
      "application-autoscaling:RegisterScalableTarget",
      "application-autoscaling:DeregisterScalableTarget",
      "application-autoscaling:PutScalingPolicy",
      "application-autoscaling:DescribeScalableTargets",
      "application-autoscaling:DescribeScalingPolicies",
      "application-autoscaling:ListTagsForResource",
      "application-autoscaling:TagResource",
      "cloudwatch:PutMetricAlarm","cloudwatch:DeleteAlarms","cloudwatch:DescribeAlarms"
    ]},
    {Sid:"PassCollectorTaskExecutionRole",Effect:"Allow",Action:"iam:PassRole",
     Resource:$executionRoleArn,
     Condition:{StringEquals:{"iam:PassedToService":"ecs-tasks.amazonaws.com"}}}
  ]
}' > odigos-collector-deployment-policy.json

aws iam put-role-policy --role-name "$COLLECTOR_DEPLOYMENT_ROLE_NAME" \
  --policy-name OdigosCollectorDeployment \
  --policy-document file://odigos-collector-deployment-policy.json
export COLLECTOR_DEPLOYMENT_ROLE_ARN=$(aws iam get-role \
  --role-name "$COLLECTOR_DEPLOYMENT_ROLE_NAME" --query Role.Arn --output text)
printf 'Collector deployment role: %s\n' "$COLLECTOR_DEPLOYMENT_ROLE_ARN"
```

### 5.1. Permission boundary and network placement

The deployment role needs the six EC2 describe actions above even in manual mode: the connector uses them to validate VPC, subnet, route, peering, and security-group placement. Automatic mode additionally needs the write actions. A selected subnet must have a usable egress route. If you supply a subnet with a nonstandard route through an interface endpoint, inspect the reported placement issues before applying the Collector infrastructure.

The policy grants some AWS APIs on `Resource: "*"` because their list/describe operations do not support resource-level IAM scoping. It limits regional actions to `$AWS_REGION` through `aws:RequestedRegion`. The trust policy restricts who can receive the role's permissions, and `iam:PassRole` is scoped separately. Apply organization SCPs, permission boundaries, and resource tags as required by your account.

If you plan to opt into automatic scaling (`scaling.mode: auto`), ensure the account has the ECS Application Auto Scaling service-linked role. Create it only when missing:

```sh
if aws iam get-role --role-name AWSServiceRoleForApplicationAutoScaling_ECSService >/dev/null 2>&1; then
  printf 'Application Auto Scaling service-linked role already exists.\n'
else
  aws iam create-service-linked-role --aws-service-name ecs.application-autoscaling.amazonaws.com
fi
```

## 6. Enable automatic Collector deployment on the connector

This AWS connector currently supports one Collector type. The patch sets its management and deployment capabilities. A connector with only management enabled can observe a customer-installed Collector but will not mutate it.

```sh
kubectl -n "$CONNECTOR_NAMESPACE" patch odigoscloudconnector "$CONNECTOR_NAME" \
  --type=merge \
  -p '{"spec":{"collectors":[{"type":"aws.fargate-collector","management":true,"deployment":true}]}}'
kubectl -n "$CONNECTOR_NAMESPACE" get odigoscloudconnector "$CONNECTOR_NAME" -o json |
  jq -e '.spec.collectors[] | select(.type == "aws.fargate-collector" and .management == true and .deployment == true)'
```

## 7. Create the Collector through GraphQL

This mutation configures a Collector in the selected region and the VPC you provided in step 3, lets the connector select the subnets within that VPC, and passes both pre-created role ARNs. Keep `subnetIds` omitted to use that selection. The connector validates that it can assume `deploymentRoleRef` before accepting the create. IAM policy propagation can take a short time; retry the same mutation if AWS has not propagated a newly created role yet. The command uses the `ASSIGN_PUBLIC_IP` value chosen in step 3. A public task IP affects outbound access; it does not change the NLB from internal to public. The report's `details.subnetIds` shows the selected subnets after they have been deployed.

```sh
cat > create-managed-collector.graphql <<'GRAPHQL'
mutation CreateManagedCollector($input: CreateManagedCollectorInput!) {
  createManagedCollector(input: $input) {
    region
    status
    message
    desired {
      deploymentRoleRef
      providerOverrides {
        ... on AwsManagedCollectorProviderOverrides {
          executionRoleArn
          clusterRef
          assignPublicIp
          subnetIds
        }
      }
    }
    details {
      ... on AwsManagedCollectorDetails { subnetIds }
    }
  }
}
GRAPHQL

jq -n --rawfile query create-managed-collector.graphql \
  --arg region "$AWS_REGION" \
  --arg role "$COLLECTOR_DEPLOYMENT_ROLE_ARN" \
  --arg network "$COLLECTOR_VPC_ID" \
  --arg cluster "$ECS_CLUSTER_ARN" \
  --arg execution "$TASK_EXECUTION_ROLE_ARN" \
  --argjson publicIp "$ASSIGN_PUBLIC_IP" \
  '{query:$query,variables:{input:{region:$region,deploymentRoleRef:$role,networkId:$network,
    providerOverrides:{aws:{clusterRef:$cluster,executionRoleArn:$execution,
      assignPublicIp:$publicIp}}}}}' > create-managed-collector.json

collector_mutation create-managed-collector.json |
  jq -e 'if .errors then error(.errors | tostring) else .data.createManagedCollector end'
```

The response can initially be `Pending` or `Deploying`: infrastructure convergence is asynchronous. It should echo the deployment role, task execution role, and cluster you supplied.

The ECS task execution role must be able to read the Collector licence secret; the role created in step 4 already grants `secretsmanager:GetSecretValue` on this connector's Collector secrets. The connector stores that existing `ODIGOS_ONPREM_TOKEN` as the ECS private-registry JSON object (`username: odigos`, `password: <token>`), references the object through the container's `repositoryCredentials`, and selects its `password` field for the Collector environment variable.

For an existing managed Collector, deploy the connector build containing this support. Reconciliation migrates the existing licence secret to the JSON format, registers a new task-definition revision with registry credentials, and updates the ECS service so Fargate replaces the tasks. ECS task definitions are immutable; editing the current revision or logging in with Docker on your workstation does not update the service. The migration uses the token the connector already holds, so you do not need to add a new create-mutation field or manually copy the token into another secret.

If a task reports `CannotPullContainerError` with `401 Unauthorized` from the registry token endpoint, verify the **active ECS service revision**. An HTTP 401 means the task reached the registry; it is an authentication failure rather than an egress timeout. The current revision must show a `repositoryCredentials.credentialsParameter` secret ARN for the `collector` container. Container environment secrets alone cannot authenticate an image pull.

```sh
export COLLECTOR_TASK_DEFINITION_ARN=$(aws ecs describe-services --region "$AWS_REGION" \
  --cluster "$ECS_CLUSTER_ARN" --services "odigos-collector-$AWS_REGION" \
  --query 'services[0].taskDefinition' --output text)
aws ecs describe-task-definition --region "$AWS_REGION" \
  --task-definition "$COLLECTOR_TASK_DEFINITION_ARN" \
  --query 'taskDefinition.{executionRoleArn:executionRoleArn,containers:containerDefinitions[].{name:name,image:image,repositoryCredentials:repositoryCredentials,secrets:secrets}}' \
  --output json
```

This prints secret references, not their values. If `repositoryCredentials` is absent, the ECS service still points at a revision created by connector code without private-registry support. Restart or redeploy the connector build containing that support, then allow reconciliation to register and activate a new task-definition revision. If it is present and pulls still return 401, check that its Secrets Manager JSON has valid registry credentials and that the task execution role can read that secret. A service can temporarily have running tasks on an older revision while a newer deployment fails; compare the service's deployments and running tasks before concluding which revision is working:

```sh
aws ecs describe-services --region "$AWS_REGION" --cluster "$ECS_CLUSTER_ARN" \
  --services "odigos-collector-$AWS_REGION" \
  --query 'services[0].{taskDefinition:taskDefinition,desired:desiredCount,running:runningCount,deployments:deployments[].{status:status,taskDefinition:taskDefinition,desired:desiredCount,running:runningCount,pending:pendingCount}}' \
  --output json
```

For an existing Collector explicitly configured with public subnet IDs and `assignPublicIp: false`, update it in place instead of running the create mutation again:

```sh
jq -n --arg region "$AWS_REGION" '{
  query:"mutation UpdateManagedCollector($input: UpdateManagedCollectorInput!) { updateManagedCollector(input: $input) { region status message desired { providerOverrides { ... on AwsManagedCollectorProviderOverrides { assignPublicIp subnetIds } } } } }",
  variables:{input:{region:$region,providerOverrides:{aws:{assignPublicIp:true}}}}
}' > update-managed-collector-public-ip.json
collector_mutation update-managed-collector-public-ip.json |
  jq -e 'if .errors then error(.errors | tostring) else .data.updateManagedCollector end'
```

This retains the existing subnet IDs and changes the ECS task network configuration on reconciliation. Set `subnetIds: []` in an update to clear pinned subnet IDs and return to automatic subnet selection.

## 8. Watch the Collector and inspect errors

```sh
jq -n --arg region "$AWS_REGION" '{
  query:"query GetManagedCollectorReport($region: String!) { managedCollectorReport(region: $region) { region status liveness message issues { code message } desired { deploymentRoleRef providerOverrides { ... on AwsManagedCollectorProviderOverrides { executionRoleArn clusterRef assignPublicIp subnetIds } } } current { configHash configPushedAt firstReadyAt scaling { desiredCount runningCount } endpoint { grpc http } } details { ... on AwsManagedCollectorDetails { revisionRef subnetIds } } } }",
  variables:{region:$region}
}' > get-managed-collector.json

curl -fsS -H 'Content-Type: application/json' \
  --data-binary @get-managed-collector.json "$GRAPHQL_URL" |
  jq -e 'if .errors then error(.errors | tostring) else .data.managedCollectorReport end'
```

Repeat the query until the status is `Ready`, liveness is `live`, and the endpoint fields are populated. ECS `runningCount` only says tasks started; it does not prove the Collector connected over OpAMP or accepted configuration. `configHash` and `configPushedAt` show whether a configuration was accepted over OpAMP; `firstReadyAt` shows whether the first ready transition occurred. Check `issues` and `message` if it stalls. The role assumption check at create time proves trust and `sts:AssumeRole`; subsequent AWS service denials appear in the report.

To move an automatically deployed Collector to another VPC, update only `networkId` in one mutation. The connector records the target VPC, removes the old stack and its network-bound credential, then provisions in the new VPC. The report shows `CollectorCloudResourcesMoving` until the old stack is gone. AWS interface cleanup may take several reconcile passes. Make any other setting changes in a separate update after the move. Customer-owned or adopted infrastructure is not deleted by a network move.

`managedCollectorMetrics.live` has a different meaning from the report's `liveness`: with `refresh:false` it is always `false` because the query returns the stored metrics snapshot. Use `refresh:true` to wait up to five seconds for a new periodic snapshot; `live:true` means one arrived during that query. A `null` counter means that metric was absent from the Collector's own telemetry, not that its value was zero. CPU, memory, and uptime can be present while receiver/exporter counters are absent, for example before any traffic reaches a pipeline. If they stay absent after traffic is sent, inspect the applied Collector configuration and the workload-to-NLB path.

### Inspect the actual pipeline

Use **View pipeline** on the Collector page. **Currently running** requests full state over each
connected Collector's OpAMP session and displays its effective YAML with the report time and instance
UID. **New configuration** renders the current connector intent using the same path as OpAMP pushes;
it is not evidence that the Collector accepted it. **Rejected** shows that generated document and the
validation error only when the Collector rejected that exact hash. Credentials are redacted before
the configuration reaches the browser; the displayed hash identifies the original unredacted YAML.

The versioned `managedCollectorConfigurations(region: String!)` query backs this view. No configuration
snapshot table or database history is used. Reports are held only for active OpAMP connections and
discarded at disconnect. A refresh waits up to three seconds for a full-state response, falling back
to that connection's last report with its original timestamp. If no effective configuration was
reported, the running view is unavailable rather than substituting the new configuration.

## 9. Configure export and route workloads

A running Collector is only an ingress endpoint until you configure where it exports. A regional Collector destination is the downstream exporter; a connector destination of type odigos-managed-collector is the separate marker that routes workloads to that Collector. With no downstream destination, the Collector accepts signals into nop pipelines and discards them. Configure export before enabling workload routing.

List the downstream types the Collector can render and any destinations already configured for this region:

~~~sh
jq -n --arg region "$AWS_REGION" '{
  query:"query CollectorExportOptions($region: String!) { getManagedCollectorDestinationTypes { type displayName supportedSignals { traces metrics logs } fields { name secret } } listManagedCollectorDestinations(region: $region) { name type signals disabled } }",
  variables:{region:$region}
}' > collector-export-options.json
curl -fsS -H 'Content-Type: application/json' \
  --data-binary @collector-export-options.json "$GRAPHQL_URL" |
  jq -e 'if .errors then error(.errors | tostring) else .data end'
~~~

For example, to send all three signals to an OTLP/gRPC endpoint, set the real downstream endpoint and create one Collector destination. Choose the destination type and required config fields from the type query above for other backends. Supply any required credentials through your approved secret-handling process; do not commit a JSON request containing them.

~~~sh
export COLLECTOR_EXPORT_OTLP_ENDPOINT=otlp.example.internal:4317
jq -n --arg region "$AWS_REGION" --arg endpoint "$COLLECTOR_EXPORT_OTLP_ENDPOINT" '{
  query:"mutation AddCollectorExport($input: CreateManagedCollectorDestinationInput!) { createManagedCollectorDestination(input: $input) { region name type signals disabled } }",
  variables:{input:{region:$region,name:"primary",type:"otlp",
    config:{OTLP_GRPC_ENDPOINT:$endpoint},signals:["TRACES","METRICS","LOGS"]}}
}' > create-collector-export.json
collector_mutation create-collector-export.json |
  jq -e 'if .errors then error(.errors | tostring) else .data.createManagedCollectorDestination end'
~~~

Once the report shows an endpoint and the Collector has reported ready, inspect connector destinations, then create one enabled regional routing marker if it does not already exist:

~~~sh
curl -fsS -H 'Content-Type: application/json' \
  --data '{"query":"query { destinations { name type region signals disabled } }"}' \
  "$GRAPHQL_URL" |
  jq -e 'if .errors then error(.errors | tostring) else .data.destinations end'

jq -n --arg region "$AWS_REGION" '{
  query:"mutation RouteViaCollector($input: CreateDestinationInput!) { createDestination(input: $input) { name type region signals disabled } }",
  variables:{input:{name:("regional-collector-" + $region),type:"odigos-managed-collector",
    region:$region,signals:["TRACES","METRICS","LOGS"],config:{}}}
}' > create-collector-route.json
collector_mutation create-collector-route.json |
  jq -e 'if .errors then error(.errors | tostring) else .data.createDestination end'
~~~

The marker is a workload routing choice, not a Collector exporter. Its signals are used when there is no global OTLP destination; otherwise the global destination's signal selection is used. The connector allows one marker per region. Enable instrumentation sources through the normal source flow after configuring the marker. Enabled Fargate services and VPC-attached Lambda functions contribute their source security groups to Collector ingress. The connector routes them only after the Collector has been ready once and AWS discovery proves subnet routes and security-group access to every published OTLP port. Another VPC needs non-overlapping CIDRs, active same-region peering, and reciprocal routes in the selected workload and Collector subnets. The connector does not create those network connections. For an external sender outside connector discovery, configure providerOverrides.aws.additionalSecurityGroupIds or ingress.additionalCidrs through updateManagedCollector; these grant ingress but do not configure that sender or qualify it for automatic workload routing. Do not edit the managed security group directly, because reconciliation removes rules outside its desired set.

A ready, reachable regional Collector takes precedence over a global direct OTLP destination. A global destination provides fallback when the Collector route is unavailable and a direct route during staged cutover. Without one, eligible workloads route to the Collector immediately after it first reports Ready; workloads that cannot reach it have no export route. When a global destination exists, the first eligible instrumented workload starts a one-minute warning window after the Collector first reports Ready and a managed-Collector destination selects its region. The report shows the announcement, the count currently routed through the Collector, and any failed cutovers; the connector retries failures without holding back untouched workloads. The configured regional batch is shared between Fargate and Lambda when both are enabled; the spare slot in an odd-sized batch alternates between them on successive passes. A Lambda without a VPC configuration cannot use this internal NLB. Disabling the regional destination restores direct export when a global destination exists; later Collector health changes alone do not automatically move workloads back.

`updateManagedCollector(enabled:false)` stops provisioning. The default retention policy keeps the existing Collector running. An admin may set `retention.idleScaleToZeroAfterHours` and/or `retention.idleDeleteAfterHours` in create or update; the connector starts the idle clock when the region is disabled or has no enabled sources, then parks or removes connector-owned infrastructure after the configured grace. Zero keeps that action disabled. Manual customer-owned infrastructure is never removed by this policy.

Inspect workload reports after enabling sources; their issues explain missing destinations or unreachable Collector routes:

~~~sh
curl -fsS -H 'Content-Type: application/json' \
  --data '{"query":"query { workloadReports { serviceName kind region desired { enabled sourceEnabled } current { state instrumented } progress { stage message } issues { code message } } }"}' \
  "$GRAPHQL_URL" |
  jq -e 'if .errors then error(.errors | tostring) else .data.workloadReports end'
~~~

If customer IaC has left instrumentation on a workload without an enabled source, its report shows an error even when that workload type uses automatic mode; remove the instrumentation from IaC or restore the source. Connector-owned instrumentation may remain briefly during automatic rollback without this error. A fresh own-metrics snapshot alone does not prove that workload signals reached the Collector. After generating known workload traffic, query managedCollectorMetrics with refresh:true and check accepted and sent counters; absent series remain null.

~~~sh
jq -n --arg region "$AWS_REGION" '{
  query:"query CollectorTraffic($region: String!) { managedCollectorMetrics(region: $region, refresh: true) { observedAt live acceptedSpans acceptedMetricPoints acceptedLogRecords sentSpans sentMetricPoints sentLogRecords failedSpans failedMetricPoints failedLogRecords } }",
  variables:{region:$region}
}' > get-collector-traffic.json
curl -fsS -H 'Content-Type: application/json' \
  --data-binary @get-collector-traffic.json "$GRAPHQL_URL" |
  jq -e 'if .errors then error(.errors | tostring) else .data.managedCollectorMetrics end'
~~~

## 10. Tear down an automatically deployed Collector

The connector records a regional cloud-resource state separately from the Collector report row. A Collector that was declared but never reached cloud provisioning can be removed by the same teardown mutation: it deletes the regional desired configuration and local records without making AWS delete calls. This also removes the region from `managedCollectorReports` and prevents a later reconcile from creating it. The persisted state becomes `destroyed` with a detail saying no cloud resources were created.

Once provisioning has started, the connector records `provisioning`, then `created` when cloud resources are recorded. A retryable AWS teardown is recorded as `tearing-down`; an uncertain incomplete teardown is recorded as `orphaned`. A transient provisioning error keeps the state at `provisioning` or `created` and appears as an issue. An old record with no saved infrastructure references is treated as uncertain unless it also has no cloud identity and no deployment parameters. Inspect possible cloud resources before recovering an uncertain state. Connector shutdown uses the same state distinction and waits for incomplete cloud teardown.

Teardown deletes the Collector's ECS service, stops any service tasks still running while it is `DRAINING`, and deletes the internal NLB before deleting its security group. AWS can take several minutes to release the network interfaces those resources used. The connector keeps the Collector record and retries until the ECS service is inactive and the security group is gone. The mutation returns `deleted:false` while cleanup is in progress. The report shows `Deleting` during the first 30 minutes, then `Error` with `CollectorTeardownTimedOut` if AWS still blocks cleanup. Retries continue after that timeout; the record and its refs remain until deletion succeeds. Check the report again until cleanup completes; do not delete the security group or Collector record by hand while teardown is in progress.

```sh
jq -n --arg region "$AWS_REGION" '{
  query:"mutation TeardownManagedCollector($input: TeardownManagedCollectorInput!) { teardownManagedCollector(input: $input) { region deleted message retained } }",
  variables:{input:{region:$region}}
}' > teardown-managed-collector.json

collector_mutation teardown-managed-collector.json |
  jq -e 'if .errors then error(.errors | tostring) else .data.teardownManagedCollector end'
```

Repeat after AWS releases the interfaces, until the response has `"deleted": true`. If the dependency remains after several minutes, inspect the security group's attached network interfaces before changing anything:

```sh
export COLLECTOR_SG_ID=sg-09f8f44533f2b8594
aws ec2 describe-network-interfaces --region "$AWS_REGION" \
  --filters "Name=group-id,Values=$COLLECTOR_SG_ID" \
  --query 'NetworkInterfaces[].{Id:NetworkInterfaceId,Status:Status,Description:Description,RequesterManaged:RequesterManaged}' \
  --output table
```

An ECS or NLB interface that is still deleting is usually transient. If no interface appears but deletion is still refused, check for another security group referencing this one or another customer-owned dependency, and investigate that reference before retrying.

## 11. Manual mode: customer-owned infrastructure

Complete steps 1–4, then create a separate read-only observation role with these commands. Set `deployment:false` while keeping `management:true`, and supply this role as the **same required `deploymentRoleRef` field** to `createManagedCollector`. The connector assumes it for placement and observation only. Apply the generated CloudFormation or Terraform with your own IaC principal. The connector discovers the internal NLB endpoint and never creates or deletes customer-owned infrastructure.

```sh
export COLLECTOR_DEPLOYMENT_ROLE_NAME=OdigosCollectorObservationRole
jq -n --arg principal "$CONNECTOR_PRINCIPAL_ARN" '{
  Version:"2012-10-17",
  Statement:[{Effect:"Allow",Principal:{AWS:$principal},Action:"sts:AssumeRole"}]
}' > odigos-collector-observation-trust.json
if aws iam get-role --role-name "$COLLECTOR_DEPLOYMENT_ROLE_NAME" >/dev/null 2>&1; then
  printf 'Role already exists; verify its trust policy before attaching the observation policy.\n'
  aws iam get-role --role-name "$COLLECTOR_DEPLOYMENT_ROLE_NAME" --query 'Role.AssumeRolePolicyDocument' --output json
else
  aws iam create-role --role-name "$COLLECTOR_DEPLOYMENT_ROLE_NAME" \
    --assume-role-policy-document file://odigos-collector-observation-trust.json
fi

jq -n --arg region "$AWS_REGION" '{Version:"2012-10-17",Statement:[{
  Effect:"Allow",Resource:"*",Condition:{StringEquals:{"aws:RequestedRegion":$region}},Action:[
    "ecs:DescribeClusters","ecs:DescribeServices","ecs:ListTasks","ecs:DescribeTasks","ecs:DescribeTaskDefinition",
    "elasticloadbalancing:DescribeLoadBalancers","elasticloadbalancing:DescribeListeners",
    "elasticloadbalancing:DescribeTargetGroups","elasticloadbalancing:DescribeTargetHealth",
    "ec2:DescribeVpcs","ec2:DescribeSubnets","ec2:DescribeSecurityGroups",
    "ec2:DescribeRouteTables","ec2:DescribeVpcPeeringConnections"
  ]
}]}' > odigos-collector-observation-policy.json
aws iam put-role-policy --role-name "$COLLECTOR_DEPLOYMENT_ROLE_NAME" \
  --policy-name OdigosCollectorObservation \
  --policy-document file://odigos-collector-observation-policy.json
export COLLECTOR_DEPLOYMENT_ROLE_ARN=$(aws iam get-role \
  --role-name "$COLLECTOR_DEPLOYMENT_ROLE_NAME" --query Role.Arn --output text)
printf 'Collector observation role: %s\n' "$COLLECTOR_DEPLOYMENT_ROLE_ARN"
```

```sh
kubectl -n "$CONNECTOR_NAMESPACE" patch odigoscloudconnector "$CONNECTOR_NAME" \
  --type=merge \
  -p '{"spec":{"collectors":[{"type":"aws.fargate-collector","management":true,"deployment":false}]}}'
```

Run the step 7 mutation with the customer-provisioned manual observation role ARN in `COLLECTOR_DEPLOYMENT_ROLE_ARN`, then fetch the exact regional installation artifacts:

Create the log group `/odigos/collector/$CONNECTOR_NAME` with retention, and a security group in `COLLECTOR_VPC_ID`. Allow TCP 4317 and 4318 from the workloads that will send telemetry, TCP 13133 from the same security group for target health checks, and outbound access to the image registry, Secrets Manager, the OpAMP endpoint, and export destinations. Choose subnets with NAT egress or set `assignPublicIp:true` when using internet-gateway-only subnets. The execution role from step 4 is scoped to this log group and secrets under `odigos/collector/$CONNECTOR_NAME/`.

Create the credential secret first to obtain its complete AWS ARN. The issuer binds the one-time credential to the selected region, VPC and secret ARN; it does not populate the secret.

```sh
export CREDENTIAL_SECRET_ARN=$(aws secretsmanager create-secret --region "$AWS_REGION" \
  --name "odigos/collector/$CONNECTOR_NAME/$AWS_REGION/credential" --query ARN --output text)
jq -n --arg region "$AWS_REGION" --arg network "$COLLECTOR_VPC_ID" \
  --arg secret "$CREDENTIAL_SECRET_ARN" '{
  query:"mutation Issue($input: IssueManagedCollectorCredentialInput!) { issueManagedCollectorCredential(input: $input) { credentialId authorizationHeader secretRef } }",
  variables:{input:{region:$region,networkId:$network,secretRef:$secret}}
}' > issue-collector-credential.json
export COLLECTOR_AUTH_HEADER=$(collector_mutation issue-collector-credential.json |
  jq -er 'if .errors then error(.errors | tostring) else .data.issueManagedCollectorCredential.authorizationHeader end')
aws secretsmanager put-secret-value --region "$AWS_REGION" \
  --secret-id "$CREDENTIAL_SECRET_ARN" --secret-string "$COLLECTOR_AUTH_HEADER" >/dev/null
unset COLLECTOR_AUTH_HEADER
```

Create the licence secret as registry JSON. ECS uses the JSON object for the image pull, and the Collector reads its `password` field as `ODIGOS_ONPREM_TOKEN`. Do not put the token on a command line that your shell records in history.

```sh
read -rs ODIGOS_ONPREM_TOKEN
printf '\n'
export ODIGOS_ONPREM_TOKEN
export LICENSE_SECRET_ARN=$(jq -n '{username:"odigos",password:env.ODIGOS_ONPREM_TOKEN}' |
  aws secretsmanager create-secret --region "$AWS_REGION" \
    --name "odigos/collector/$CONNECTOR_NAME/$AWS_REGION/license" \
    --secret-string file:///dev/stdin --query ARN --output text)
unset ODIGOS_ONPREM_TOKEN
```

The AWS CLI may not accept `file:///dev/stdin` for `--secret-string` on every platform. If it does not, use your approved secret manager workflow to create the same JSON value without exposing the token in a process argument.

```sh
jq -n --arg region "$AWS_REGION" '{
  query:"query GetManagedCollectorInstructions($region: String!) { managedCollectorInstructions(region: $region) { region deploymentRoleRef mode notes documents { id variant title steps content unresolved { placeholder description } } } }",
  variables:{region:$region}
}' > get-managed-collector-instructions.json
curl -fsS -H 'Content-Type: application/json' \
  --data-binary @get-managed-collector-instructions.json "$GRAPHQL_URL" |
  jq -e 'if .errors then error(.errors | tostring) else .data.managedCollectorInstructions end'
```

Choose one returned document, fill its parameters or variables with the cluster, selected subnets, security group, execution role, log group and both secret ARNs, then apply it with your own IaC principal. The generated task definition includes repository credentials, the licence JSON selector, a 120-second health grace period, public-IP placement, ownership tags and optional bootstrap secret. Do not set `endpointOverride` for the generated internal NLB. Watch `managedCollectorReport` until it reports Ready and an endpoint; use its issues to diagnose image pull, secret access, OpAMP egress or unhealthy targets.

The document templates, steps and notes returned by `managedCollectorInstructions` are authored under `collectors.instructions` in `connectors/spec/data/aws.yaml`. The connector fills in the selected region, image, resource names, placement and scaling values when it serves the instructions.

To rotate, issue a new credential with `rotate:true`, update the credential secret, and force a new ECS service deployment. After the replacement connects, revoke the old credential with the admin mutation `revokeManagedCollectorCredential(input:{region,credentialId})`. To remove manual mode, call `teardownManagedCollector`; it revokes connector credentials and removes the declaration while returning retained customer resource references. Then remove the CloudFormation stack or Terraform resources with your own IaC workflow.

To change the manual Collector's `networkId`, update the declaration, move the customer-owned IaC to the new VPC, issue a new credential bound to that VPC and redeploy the ECS service after writing the new header to its secret. The connector revokes the old network-bound credential when the declaration changes; it does not delete or move the customer-owned stack.

## API behavior for every provider

- `CreateManagedCollectorInput.deploymentRoleRef` is required in manual and automatic mode.
- `UpdateManagedCollectorInput.deploymentRoleRef` can rotate that identity without recreating the Collector.
- The report's desired state and `managedCollectorInstructions` expose the configured reference.
- Each provider validates its own identity format and uses the supplied identity for its Collector reads and writes. AWS implements this with STS AssumeRole; providers without a Collector implementation are unaffected until they add one.
