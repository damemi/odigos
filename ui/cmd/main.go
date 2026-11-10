// Command odigos-ui-enterprise wraps the OSS Odigos UI (frontend/server) with the
// enterprise on-prem token gate and the in-process MCP server at /mcp. Everything
// else — GraphQL, routes, embedded webapp — is inherited from the frontend module.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"
	ctrl "sigs.k8s.io/controller-runtime"

	"k8s.io/klog/v2"

	"github.com/odigos-io/odigos/ui/mcp"
	"github.com/odigos-io/odigos/ui/mcp/k8s"
	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	enterprisedistros "github.com/odigos-io/odigos/distros/enterprise"
	"github.com/odigos-io/odigos/frontend/server"
	"github.com/odigos-io/odigos/frontend/services"
	"github.com/odigos-io/odigos/odigosauth"
)

func main() {
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flags := server.ParseFlags()

	commonlogger.Init(os.Getenv("ODIGOS_LOG_LEVEL"), "ui")
	logger := commonlogger.ToLogr()
	ctrl.SetLogger(logger)
	klog.SetLogger(logger)
	log := commonlogger.LoggerCompat().With("subsystem", "startup")

	// Enterprise gate — crash on missing/invalid on-prem (or cloud) token.
	// Same posture as the enterprise instrumentor and central-backend mains.
	odigosauth.AssertEnterpriseOdigosToken(commonlogger.LoggerCompat())

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer func() {
		signal.Stop(sigCh)
		cancel()
	}()

	go common.StartPprofServer(ctx, logger, int(k8sconsts.DefaultPprofEndpointPort))

	// Token expiration watchdog: when the on-prem token expires, exit so the
	// kubelet restarts us. The pod will crash-loop until a new token is set.
	expirationCh, err := odigosauth.TokenExpirationChannel()
	if err != nil {
		log.Error("Getting a token expiration channel event", "err", err)
		os.Exit(1)
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-expirationCh:
			log.Info("On-prem token expired")
			os.Exit(1)
		}
	}()

	deps, err := server.Bootstrap(ctx, flags, logger)
	if err != nil {
		log.Error("Frontend server bootstrap failed", "err", err)
		os.Exit(1)
	}

	// Bootstrap defaults to the community provider. Replace it with the enterprise
	// one so the UI reports the distros the enterprise instrumentor actually injects.
	deps.DistrosProvider, err = enterprisedistros.NewEnterpriseProvider()
	if err != nil {
		log.Error("Loading enterprise distros failed", "err", err)
		os.Exit(1)
	}

	wg, err := server.StartBackground(ctx, deps)
	if err != nil {
		log.Error("Starting background Frontend server failed", "err", err)
		os.Exit(1)
	}

	// MCP is mounted by default. The endpoint is in-cluster only (not Service-
	// exposed), the UI pod is already gated behind the enterprise on-prem token,
	// and write tools are blocked in read-only access mode — so it is a safe
	// default. Set ui.mcp.enabled=false (OdigosConfiguration.mcpEnabled) to disable.
	var extraMounts []func(*gin.Engine, *server.Deps)
	if mcpEnabled(ctx) {
		extraMounts = append(extraMounts, mountMCP)
		log.Info("MCP server enabled, mounting /mcp")
	}

	r, err := server.BuildRouter(ctx, deps, server.RouterOpts{ExtraMounts: extraMounts})
	if err != nil {
		log.Error("building router failed", "err", err)
		os.Exit(1)
	}

	if err := server.ServeAndWait(cancel, deps, r, sigCh, wg); err != nil {
		os.Exit(1)
	}
}

// mcpEnabled reports whether to mount the MCP server, read from the effective
// Odigos configuration (helm: ui.mcp.enabled). A nil McpEnabled means enabled,
// matching the OdigosConfiguration contract (default on).
func mcpEnabled(ctx context.Context) bool {
	cfg, err := services.GetOdigosConfiguration(ctx)
	if err != nil {
		// Config unreadable at startup: default on (still gated by the on-prem
		// token and the read-only access mode).
		return true
	}
	return cfg.McpEnabled == nil || *cfg.McpEnabled
}

// mountMCP wires the in-process MCP server onto the gin router. It lives here, in
// the enterprise UI binary, on purpose: MCP is an enterprise-only surface, so
// mounting it in this binary (the community UI never calls mountMCP) is what gates
// it to enterprise. It runs in the same process as the UI and reuses the frontend's
// shared kube client plus the bootstrap deps (metrics / prometheus / profile-store)
// the k8s-only tools need.
func mountMCP(r *gin.Engine, deps *server.Deps) {
	r.Any("/mcp", gin.WrapH(mcp.Handler(k8s.Deps{
		Logger:          deps.Logger,
		MetricsConsumer: deps.OdigosMetrics,
		PromAPI:         deps.PromAPI,
		K8sCacheClient:  deps.K8sCacheClient,
		ProfileStore:    deps.ProfileStore,
	})))
}
