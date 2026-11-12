// Package detector implements WORKLOAD discovery for the AWS connector — Lambda functions and Fargate
// tasks, the resources instrumented directly via a Source. Each detector enumerates one type across the
// account's enabled regions and persists the workload store inventory the base reports as WorkloadReports.
// Compute-platform (managed-infra) discovery lives in the sibling computeplatform/detector package.
package detector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	connlog "github.com/odigos-io/odigos/connectors/base/log"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/awsclient"
	store "github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

var log = connlog.Subsystem("aws-workload-detector")

// LambdaDetector discovers Lambda functions across the account's enabled regions.
type LambdaDetector struct {
	store *store.Store
	cache *awsclient.Cache
	// tags returns the current discovery tag selectors (DB-backed live config): when non-empty, only
	// functions carrying ALL of these tag key/values are discovered (the customer's blast-radius limiter).
	// It is a getter so a config change applies on the next cycle without a restart.
	tags func() map[string]string
}

// NewLambdaDetector builds the Lambda detector over the workload store and shared config cache. tags is a
// getter for the live discovery tag filter (nil/empty discovers all functions).
func NewLambdaDetector(st *store.Store, cache *awsclient.Cache, tags func() map[string]string) *LambdaDetector {
	return &LambdaDetector{store: st, cache: cache, tags: tags}
}

func (d *LambdaDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	regions, err := d.cache.EnabledRegions(ctx)
	if err != nil {
		return baseapi.DetectResult{}, fmt.Errorf("enumerate enabled regions: %w", err)
	}
	tags := d.tags()
	log.Info("lambda detector starting", "regions", len(regions), "tag_filter", len(tags) > 0)
	var anyErr error
	authed := false
	for _, region := range regions {
		// The prune cutoff comes from the database clock, the one last_seen_at is written with.
		scanStart, err := d.store.Now(ctx)
		if err != nil {
			anyErr = fmt.Errorf("region %s: read database clock: %w", region, err)
			continue
		}
		cfg, err := d.cache.Config(ctx, region)
		if err != nil {
			anyErr = err
			continue
		}
		lambdas, err := discoverLambda(ctx, cfg, region, tags)
		if err != nil {
			anyErr = err
			log.Error("lambda region scan failed", "region", region, "error", err)
			continue
		}
		authed = true
		upsertFailed := false
		for _, fn := range lambdas {
			if err := d.store.UpsertLambda(ctx, fn); err != nil {
				log.Error("upsert lambda failed", "region", region, "name", fn.FunctionName, "error", err)
				upsertFailed = true
			}
		}
		_ = d.store.UpdateDiscoveryState(ctx, region, store.KindLambda, scanStart)
		// Prune only when every discovered function persisted this cycle. Pruning after a failed upsert would
		// soft-delete a still-live function whose last_seen_at wasn't refreshed this scan, flapping its
		// instrumentation until the next clean cycle.
		if upsertFailed {
			anyErr = fmt.Errorf("region %s: one or more lambda upserts failed; skipping prune", region)
			log.Error("skipping lambda prune after upsert failure", "region", region)
		} else if err := d.store.PruneLambda(ctx, region, scanStart); err != nil {
			log.Error("prune lambda inventory failed", "region", region, "error", err)
		}
		log.Info("lambda region scan complete", "region", region, "count", len(lambdas))
	}
	// Reclaim rows soft-deleted long ago (once per cycle, account-wide) so churned inventory doesn't grow
	// unbounded. Best-effort — a purge failure never fails discovery.
	if err := d.store.PurgeDeletedLambdas(ctx, time.Now().Add(-store.SoftDeleteRetention)); err != nil {
		log.Error("purge deleted lambda inventory failed", "error", err)
	}
	return baseapi.DetectResult{Authenticated: authed}, anyErr
}

// discoverLambda lists all Lambda functions in a region and maps them to inventory records. When tags is
// non-empty it keeps only functions carrying all of the given tag key/values (spec.discovery selectors).
func discoverLambda(ctx context.Context, cfg aws.Config, region string, tags map[string]string) ([]store.LambdaFunction, error) {
	log.Info("lambda discovery starting", "region", region)
	client := lambda.NewFromConfig(cfg)
	paginator := lambda.NewListFunctionsPaginator(client, &lambda.ListFunctionsInput{})

	var out []store.LambdaFunction
	for pageNum := 1; paginator.HasMorePages(); pageNum++ {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			log.Error("lambda list functions failed", "region", region, "page", pageNum, "error", err)
			return nil, err
		}
		for _, fn := range page.Functions {
			listedTags, err := client.ListTags(ctx, &lambda.ListTagsInput{Resource: fn.FunctionArn})
			if err != nil {
				return nil, fmt.Errorf("read Lambda ownership tags for %s: %w", aws.ToString(fn.FunctionName), err)
			}
			if !tagsMatchAll(listedTags.Tags, tags) {
				continue
			}
			env := store.LambdaAttributionEnvironment(envVariables(fn.Environment), listedTags.Tags)
			out = append(out, store.LambdaFunction{
				Region:                region,
				FunctionARN:           aws.ToString(fn.FunctionArn),
				FunctionName:          aws.ToString(fn.FunctionName),
				Runtime:               string(fn.Runtime),
				Architecture:          architectures(fn.Architectures),
				RoleARN:               aws.ToString(fn.Role),
				TracingMode:           tracingMode(fn.TracingConfig),
				Instrumented:          store.OdigosInstrumented(env),
				InstrumentationOrigin: lambdaAttributionOrigin(env, listedTags.Tags),
				// ListFunctions already returns the attached layers and the function's lifecycle state, and
				// all three were being discarded. They are what separate the three situations a marker alone
				// cannot tell apart: instrumented, half-instrumented (marker kept, layer detached), and
				// mid-deploy (a change still settling, which looks half-instrumented but is not).
				Layers:           layerARNs(fn.Layers),
				State:            string(fn.State),
				LastUpdateStatus: string(fn.LastUpdateStatus),
				// The VpcConfig ListFunctions already returned and this detector was discarding. All
				// three stay empty for a function with no VpcConfig, which is the case rather than
				// a gap: it runs in an AWS-managed VPC and keeps exporting straight to the destination.
				VPCID:            vpcID(fn.VpcConfig),
				SubnetIDs:        vpcSubnets(fn.VpcConfig),
				SecurityGroupIDs: vpcSecurityGroups(fn.VpcConfig),
				Metadata: map[string]any{
					"odigos_tags": store.LambdaOwnershipTags(listedTags.Tags),
					"handler":     aws.ToString(fn.Handler),
					"memorySize":  aws.ToInt32(fn.MemorySize),
					"timeout":     aws.ToInt32(fn.Timeout),
					"version":     aws.ToString(fn.Version),
				},
			})
		}
	}

	log.Info("lambda discovery complete", "region", region, "count", len(out))
	return out, nil
}

// filterLambdaByTags keeps only functions carrying all required tag key/values. Lambda's ListFunctions
// does not return tags, so this issues one ListTags per function — done only when a tag selector is
// configured (the customer opted into narrowing discovery), so the extra calls are bounded to that case.
func filterLambdaByTags(ctx context.Context, client *lambda.Client, fns []store.LambdaFunction, required map[string]string) ([]store.LambdaFunction, error) {
	kept := make([]store.LambdaFunction, 0, len(fns))
	for _, fn := range fns {
		out, err := client.ListTags(ctx, &lambda.ListTagsInput{Resource: aws.String(fn.FunctionARN)})
		if err != nil {
			return nil, fmt.Errorf("list tags for %s: %w", fn.FunctionName, err)
		}
		if tagsMatchAll(out.Tags, required) {
			kept = append(kept, fn)
		}
	}
	return kept, nil
}

// tagsMatchAll reports whether have contains every key/value in required.
func tagsMatchAll(have, required map[string]string) bool {
	for k, v := range required {
		if have[k] != v {
			return false
		}
	}
	return true
}

// vpcID, vpcSubnets and vpcSecurityGroups read a function's VpcConfig, nil-safe. A nil VpcConfig is the
// ordinary shape for a function that was never attached to a VPC, so it is emptiness and not an error.
func vpcID(vc *lambdatypes.VpcConfigResponse) string {
	if vc == nil {
		return ""
	}
	return aws.ToString(vc.VpcId)
}

func vpcSubnets(vc *lambdatypes.VpcConfigResponse) []string {
	if vc == nil {
		return nil
	}
	return vc.SubnetIds
}

func vpcSecurityGroups(vc *lambdatypes.VpcConfigResponse) []string {
	if vc == nil {
		return nil
	}
	return vc.SecurityGroupIds
}

func architectures(arch []lambdatypes.Architecture) string {
	parts := make([]string, 0, len(arch))
	for _, a := range arch {
		parts = append(parts, string(a))
	}
	return strings.Join(parts, ",")
}

func tracingMode(tc *lambdatypes.TracingConfigResponse) string {
	if tc == nil {
		return ""
	}
	return string(tc.Mode)
}

// envVariables flattens a Lambda environment response to its variable map (nil-safe), so ownership
// detection can run over a plain map.
func envVariables(env *lambdatypes.EnvironmentResponse) map[string]string {
	if env == nil {
		return nil
	}
	return env.Variables
}

// layerARNs flattens a function's attached layers to their version ARNs, dropping any entry without one.
func layerARNs(layers []lambdatypes.Layer) []string {
	out := make([]string, 0, len(layers))
	for _, l := range layers {
		if a := aws.ToString(l.Arn); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func lambdaAttributionOrigin(env, tags map[string]string) string {
	if tags[store.TagLambdaPending] != "" || tags[store.TagLambdaManaged] != "" {
		return "connector"
	}
	return store.OdigosInstrumentationOrigin(env)
}
