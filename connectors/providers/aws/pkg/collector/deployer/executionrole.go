package deployer

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// ValidateExecutionRoleARN validates the customer-owned ECS task execution role without calling IAM.
// Odigos only passes this ARN to ECS and never creates, edits, tags, or deletes the role.
func ValidateExecutionRoleARN(ref, partition, accountID, region string) (string, []collectorstatus.Problem) {
	problem := func(detail string) (string, []collectorstatus.Problem) {
		return "", []collectorstatus.Problem{{
			Code:     ProblemExecutionRoleRequired,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("no Collector is deployed in %s: collector.regions.<region>.provider.executionRoleArn %s. "+
				"Pre-create an ECS task execution role that can read this connector's Collector secrets and write "+
				"its log streams, then set executionRoleArn to that role's ARN", region, detail),
		}}
	}

	if strings.TrimSpace(ref) == "" {
		return problem("is required")
	}
	parsed, err := arn.Parse(ref)
	if err != nil {
		return problem("is not a valid ARN")
	}
	if parsed.Partition != partition || parsed.Service != "iam" || parsed.Region != "" ||
		parsed.AccountID != accountID || !strings.HasPrefix(parsed.Resource, "role/") || parsed.Resource == "role/" {
		return problem(fmt.Sprintf("must name an IAM role in account %s and partition %s", accountID, partition))
	}
	return ref, nil
}
