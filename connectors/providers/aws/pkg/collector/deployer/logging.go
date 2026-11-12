package deployer

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
)

// LogsClient is the slice of CloudWatch Logs this package uses.
//
// There is no DescribeLogGroups here on purpose: CreateLogGroup reports an existing group as
// ResourceAlreadyExists, so create-and-tolerate is both the smaller permission set and one fewer call
// than describe-then-create — and it has no window between the two in which another replica creates it.
type LogsClient interface {
	CreateLogGroup(ctx context.Context, in *cloudwatchlogs.CreateLogGroupInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error)
	PutRetentionPolicy(ctx context.Context, in *cloudwatchlogs.PutRetentionPolicyInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error)
	TagResource(ctx context.Context, in *cloudwatchlogs.TagResourceInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.TagResourceOutput, error)
}

// LogGroupInput is what converging one region's log group depends on.
type LogGroupInput struct {
	Connector   string
	ConnectorID string
	Partition   string
	AccountID   string
	Region      string
	// RetentionDays comes from `collector.defaults.logRetentionDays`. Zero means never expire, which is
	// a real choice and not an unset value, so it is applied as such — see EnsureLogGroup.
	RetentionDays int
	Mode          string
}

// EnsureLogGroup converges the region's shared log group and returns its name.
//
// Retention is written on every pass rather than only at creation. It is live operational config,
// so a customer lowering it through `updateConfig` has to take effect without recreating
// anything — and recreating a log group to change its retention would throw away the logs.
//
// A group that already exists is NOT ownership-checked, which is the one place this package is
// deliberately more permissive than the others. A log group is append-only and holds nothing but the
// Collector's own stdout: the worst case of writing into a customer's existing group is their logs and
// ours in one place, whereas the worst case of refusing is no Collector because a group with a
// plausible name was already there. Tagging is attempted anyway, and a failure to tag is not fatal for
// the same reason.
//
// That permissiveness is exactly why the ownership marker is written only on the CREATE. Writing into a
// group Odigos did not create is a cost the paragraph above accepts; acquiring the right to DELETE one
// is not, and teardown reads that tag.
func EnsureLogGroup(ctx context.Context, api LogsClient, in LogGroupInput) (string, []collectorstatus.Problem, error) {
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		return "", problems, nil
	}

	name := LogGroupName(in.Connector)
	tags := baseapi.CollectorManagedTags(in.Connector, in.ConnectorID, in.Region, "")

	_, err := api.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(name),
		// The ownership marker goes on the CREATE and only on the create. See the already-exists branch.
		Tags: tags,
	})
	switch {
	case err == nil:
		log.Info("created collector log group", "region", in.Region, "group", name)
	case isLogGroupAlreadyExists(err):
		// Tag an existing group so one Odigos created before this code did — or one whose tags a
		// customer removed — is attributable. Best-effort: a tagging failure must not stop a Collector
		// from being provisioned over a log group that already works.
		//
		// The IDENTITY tags only, never CollectorManagedTags, and this is the one place the difference
		// bites. This branch is reached both by a group Odigos created and by a customer's own group
		// under a plausible name — the whole reason the write above is not ownership-checked — so
		// stamping `odigos-managed` here would hand Odigos deletion authority over a log group it did
		// not create, and teardown would take the customer's logs with it (the deployer writes
		// the ownership marker only on an object it created itself). Adopting is not creating, and
		// neither is writing into.
		if _, err := api.TagResource(ctx, &cloudwatchlogs.TagResourceInput{
			ResourceArn: aws.String(logGroupARN(in.Partition, in.Region, in.AccountID, name)),
			Tags:        baseapi.CollectorIdentityTags(in.Connector, in.ConnectorID, in.Region, ""),
		}); err != nil {
			log.Warn("could not tag the existing collector log group", "region", in.Region, "group", name, "error", err)
		}
	default:
		return "", nil, fmt.Errorf("create log group %s in %s: %w", name, in.Region, err)
	}

	if _, err := api.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{
		LogGroupName:    aws.String(name),
		RetentionInDays: aws.Int32(int32(in.RetentionDays)),
	}); err != nil {
		// Not fatal. The group exists and the Collector can write to it; the only loss is that logs are
		// kept for longer (or forever) than asked, which costs money rather than telemetry.
		log.Warn("could not set collector log retention", "region", in.Region, "group", name, "days", in.RetentionDays, "error", err)
		return name, []collectorstatus.Problem{{
			Code:     ProblemLogRetentionUnset,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("the Collector log group %q in %s exists but its retention could not be set to "+
				"%d days, so logs are kept under whatever policy the group already had. This costs storage, not "+
				"telemetry", name, in.Region, in.RetentionDays),
		}}, nil
	}
	return name, nil, nil
}

// logGroupARN is the ARN form TagResource wants. CreateLogGroup takes a name and TagResource takes an
// ARN, which is the whole reason this exists.
func logGroupARN(partition, region, accountID, name string) string {
	return fmt.Sprintf("arn:%s:logs:%s:%s:log-group:%s", partition, region, accountID, name)
}

func isLogGroupAlreadyExists(err error) bool {
	var e *logstypes.ResourceAlreadyExistsException
	return errors.As(err, &e)
}
