package gql

import (
	"context"
	"net/http"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/executor"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/odigos-io/agents-api/go/server/graph/generated"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var httpLog = connlog.Subsystem("gql-http")

// NewExecutor builds a gqlgen executor over the agents-api schema, used to serve GraphQL over the
// reverse WebSocket to Central (the same mechanism the VM agent uses).
func NewExecutor(central api.CentralInterface) *executor.Executor {
	es := generated.NewExecutableSchema(generated.Config{Resolvers: &RootResolver{Central: central}})
	return executor.New(es)
}

// NewHandler builds the connector's HTTP handler: the agent-api GraphQL endpoint at /graphql plus a
// /healthz liveness probe. Central reaches /graphql directly over the in-cluster Service.
func NewHandler(central api.CentralInterface) http.Handler {
	es := generated.NewExecutableSchema(generated.Config{
		Resolvers: &RootResolver{Central: central},
	})
	srv := handler.NewDefaultServer(es)
	srv.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		oc := graphql.GetOperationContext(ctx)
		opName := oc.OperationName
		opType := ""
		if oc.Operation != nil {
			if opName == "" {
				opName = oc.Operation.Name
			}
			opType = string(oc.Operation.Operation)
		}
		httpLog.Info("http graphql operation", "operation", opName, "type", opType)
		return next(ctx)
	})
	srv.AroundResponses(func(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
		resp := next(ctx)
		if resp != nil && len(resp.Errors) > 0 {
			oc := graphql.GetOperationContext(ctx)
			opName := oc.OperationName
			if oc.Operation != nil && opName == "" {
				opName = oc.Operation.Name
			}
			httpLog.Error("http graphql operation failed", "operation", opName, "errors", formatGQLErrors(resp.Errors))
		}
		return resp
	})

	mux := http.NewServeMux()
	mux.Handle("/graphql", srv)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}
