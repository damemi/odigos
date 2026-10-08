package gql

import (
	"context"
	"regexp"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/executor"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var execLog = connlog.Subsystem("gql")

// credentialIssuerContextKey is set only after the Central/CLI operation path has checked admin
// privileges. The plain in-cluster HTTP handler has no caller identity and must not reveal a
// Collector's one-time OpAMP credential.
type credentialIssuerContextKey struct{}

// RunOperation executes a GraphQL operation against the agents-api executor and returns the payload to
// embed in a transport frame's `data` field: on success the raw data object, on error a
// {"errors":[...]} map, or nil for an empty/null data result. It is shared by the Central reverse-
// WebSocket client and the odictl /ws CLI endpoint so both speak the identical wire format.
//
// isAdmin authorizes mutations: Central forwards the calling user's admin flag with every frame (as it
// does for k8s/VM proxies), and a non-admin caller (VIEWER/MAINTAINER) may read but not mutate. Without
// this the connector executed every forwarded operation ungated — a viewer could instrument or roll back
// customer Lambdas via remoteFetch. Read (query) operations are always allowed.
func RunOperation(ctx context.Context, exec *executor.Executor, query string, variables map[string]any, transport string, isAdmin bool) any {
	opName := ExtractOperationName(query)
	execLog.Debug("graphql operation start", "transport", transport, "operation", opName)

	params := &graphql.RawParams{
		Query:         query,
		Variables:     variables,
		OperationName: opName,
	}
	ctx = graphql.StartOperationTrace(ctx)
	opCtx, listErr := exec.CreateOperationContext(ctx, params)
	if len(listErr) != 0 {
		execLog.Error("graphql operation context error",
			"transport", transport,
			"operation", opName,
			"errors", formatGQLErrors(listErr),
		)
		return gqlErrorPayload(listErr)
	}
	if !isAdmin && opCtx.Operation != nil && opCtx.Operation.Operation == ast.Mutation {
		execLog.Warn("rejecting non-admin mutation", "transport", transport, "operation", opName)
		return gqlErrorPayload(gqlerror.List{gqlerror.Errorf("admin privileges are required to perform this operation")})
	}
	if isAdmin {
		ctx = context.WithValue(ctx, credentialIssuerContextKey{}, true)
	}
	respHandler, runCtx := exec.DispatchOperation(ctx, opCtx)
	resp := respHandler(runCtx)
	if resp == nil {
		execLog.Error("graphql empty response", "transport", transport, "operation", opName)
		return gqlErrorPayload(gqlerror.List{gqlerror.Errorf("empty graphql response")})
	}
	if len(resp.Errors) > 0 {
		execLog.Error("graphql operation failed",
			"transport", transport,
			"operation", opName,
			"errors", formatGQLErrors(resp.Errors),
		)
		return gqlErrorPayload(resp.Errors)
	}
	if len(resp.Data) == 0 || string(resp.Data) == "null" {
		execLog.Debug("graphql operation complete (null data)", "transport", transport, "operation", opName)
		return nil
	}
	execLog.Debug("graphql operation complete", "transport", transport, "operation", opName)
	return resp.Data
}

var operationNameRe = regexp.MustCompile(`(?:query|mutation|subscription)\s+(\w+)`)

// ExtractOperationName returns the operation name from a GraphQL document, or "" if anonymous.
func ExtractOperationName(query string) string {
	m := operationNameRe.FindStringSubmatch(query)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func gqlErrorPayload(list gqlerror.List) map[string]any {
	errs := make([]map[string]any, 0, len(list))
	for _, e := range list {
		m := map[string]any{"message": e.Message}
		if len(e.Path) > 0 {
			m["path"] = e.Path
		}
		errs = append(errs, m)
	}
	return map[string]any{"errors": errs}
}

func formatGQLErrors(list gqlerror.List) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Message)
	}
	return out
}
