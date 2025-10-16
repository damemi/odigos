package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/odigos-io/odigos/api/k8sconsts"
	cmdcontext "github.com/odigos-io/odigos/cli/pkg/cmd_context"
	"github.com/odigos-io/odigos/cli/pkg/kube"
	"github.com/odigos-io/odigos/cli/pkg/lifecycle"
	"github.com/odigos-io/odigos/cli/pkg/preflight"
	"github.com/odigos-io/odigos/cli/pkg/remote"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
	"k8s.io/apimachinery/pkg/util/version"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func enableClusterSource(cmd *cobra.Command, args []string) {
	ctx, cancel := context.WithCancel(cmd.Context())
	var uiClient *remote.UIClientViaPortForward
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	defer func() {
		if uiClient != nil {
			uiClient.Close()
		}
	}()
	defer signal.Stop(ch)

	go func() {
		<-ch
		cancel()
		if uiClient != nil {
			uiClient.Close()
		}
	}()

	excludeNamespaces, err := readAppListFromFile(excludeNamespacesFileFlag)
	if err != nil {
		fmt.Printf("\033[31mERROR\033[0m Cannot read exclude namespaces file: %+v\n", err)
		os.Exit(1)
	}

	excludeApps, err := readAppListFromFile(excludeAppsFileFlag)
	if err != nil {
		fmt.Printf("\033[31mERROR\033[0m Cannot read exclude apps file: %+v\n", err)
		os.Exit(1)
	}

	dryRun := cmd.Flag(dryRunFlagName).Changed && cmd.Flag(dryRunFlagName).Value.String() == "true"
	isRemote := cmd.Flag(remoteFlagName).Changed && cmd.Flag(remoteFlagName).Value.String() == "true"
	coolOffStr := cmd.Flag(instrumentationCoolOffFlagName).Value.String()
	coolOff, err := time.ParseDuration(coolOffStr)
	ctx = lifecycle.SetCoolOff(ctx, coolOff)
	if err != nil {
		fmt.Printf("\033[31mERROR\033[0m Invalid duration for instrumentation-cool-off: %s\n", err)
		os.Exit(1)
	}

	onlyDeployment := cmd.Flag(onlyDeploymentFlagName).Value.String()
	onlyNamespace := cmd.Flag(onlyNamespaceFlagName).Value.String()

	if (onlyDeployment != "" && onlyNamespace == "") || (onlyDeployment == "" && onlyNamespace != "") {
		fmt.Printf("\033[31mERROR\033[0m --only-deployment and --only-namespace must be set together\n")
		os.Exit(1)
	}

	fmt.Printf("About to instrument with Odigos\n")
	if dryRun {
		fmt.Printf("Dry-Run mode ENABLED - No changes will be made\n")
	}

	fmt.Printf("%-50s", "Checking if Kubernetes cluster is reachable")
	client := kube.GetCLIClientOrExit(cmd)
	fmt.Printf("\u001B[32mPASS\u001B[0m\n\n")

	if isRemote {
		uiClient, err = remote.NewUIClient(client, ctx)
		if err != nil {
			fmt.Printf("\033[31mERROR\033[0m Cannot create remote UI client: %s\n", err)
			os.Exit(1)
		}

		fmt.Println("Flag --remote is set, starting port-forward to UI pod ...")
		go func() {
			if err := uiClient.Start(); err != nil {
				fmt.Printf("\033[31mERROR\033[0m Cannot start remote UI client: %s\n", err)
				os.Exit(1)
			}
		}()

		<-uiClient.Ready()
		port, err := uiClient.DiscoverLocalPort()
		if err != nil {
			fmt.Printf("\033[31mERROR\033[0m Cannot discover local port for UI client: %s\n", err)
			os.Exit(1)
		}
		fmt.Printf("Remote client is using local port %s\n", port)
	}

	runPreflightChecks(ctx, cmd, client, isRemote)

	fmt.Printf("Starting instrumentation ...\n\n")
	instrumentCluster(ctx, client, excludeNamespaces, excludeApps, dryRun, isRemote, onlyNamespace, onlyDeployment)
}

func instrumentCluster(ctx context.Context, client *kube.Client, excludeNamespaces map[string]interface{}, excludeApps map[string]interface{}, dryRun bool, isRemote bool, onlyNamespace string, onlyDeployment string) {
	systemNs := sliceToMap(k8sconsts.DefaultIgnoredNamespaces)

	if onlyDeployment != "" {
		orchestrator, err := lifecycle.NewOrchestrator(client, ctx, isRemote)
		if err != nil {
			fmt.Printf("\033[31mERROR\033[0m Cannot create orchestrator: %s\n", err)
			os.Exit(1)
		}

		dep, err := client.AppsV1().Deployments(onlyNamespace).Get(ctx, onlyDeployment, metav1.GetOptions{})
		if err != nil {
			fmt.Printf("\033[31mERROR\033[0m Cannot get deployment %s in namespace %s: %s\n", onlyDeployment, onlyNamespace, err)
			os.Exit(1)
		}

		if dryRun {
			fmt.Printf("Dry-Run mode ENABLED - No changes will be made\n")
			return
		}

		err = orchestrator.Apply(ctx, dep)
		if err != nil {
			fmt.Printf("\033[31mERROR\033[0m Failed to instrument deployment: %s\n", err)
			os.Exit(1)
		}
		return
	}

	nsList, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Printf("\033[31mERROR\033[0m Cannot list namespaces: %s\n", err)
		os.Exit(1)
	}

	orchestrator, err := lifecycle.NewOrchestrator(client, ctx, isRemote)
	if err != nil {
		fmt.Printf("\033[31mERROR\033[0m Cannot create orchestrator: %s\n", err)
		os.Exit(1)
	}

	for _, ns := range nsList.Items {
		fmt.Printf("Instrumenting namespace: %s\n", ns.Name)
		_, excluded := excludeNamespaces[ns.Name]
		_, system := systemNs[ns.Name]
		if excluded || system {
			fmt.Printf("  - Skipping namespace due to exclusion file or system namespace\n")
			continue
		}

		err = instrumentNamespace(ctx, client, ns.Name, excludeApps, orchestrator, dryRun)
		if errors.Is(err, context.Canceled) {
			return
		}
	}
}

func instrumentNamespace(ctx context.Context, client *kube.Client, ns string, excludedApps map[string]interface{}, orchestrator *lifecycle.Orchestrator, dryRun bool) error {
	deps, err := client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Printf("  - \033[31mERROR\033[0m Cannot list deployments: %s\n", err)
		return nil
	}

	for _, dep := range deps.Items {
		fmt.Printf("  - Inspecting Deployment: %s\n", dep.Name)
		_, excluded := excludedApps[dep.Name]
		if excluded {
			fmt.Printf("    - Skipping deployment due to exclusion file\n")
			continue
		}

		if dryRun {
			fmt.Printf("    - Dry-Run mode ENABLED - No changes will be made\n")
			continue
		}

		err = orchestrator.Apply(ctx, &dep)

		if errors.Is(err, context.Canceled) {
			return err
		}
	}

	// StatefulSets
	statefulsets, err := client.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Printf("  - \033[31mERROR\033[0m Cannot list statefulsets: %s\n", err)
		return nil
	}

	for _, sts := range statefulsets.Items {
		fmt.Printf("  - Inspecting StatefulSet: %s\n", sts.Name)
		_, excluded := excludedApps[sts.Name]
		if excluded {
			fmt.Printf("    - Skipping statefulset due to exclusion file\n")
			continue
		}

		if dryRun {
			fmt.Printf("    - Dry-Run mode ENABLED - No changes will be made\n")
			continue
		}

		err = orchestrator.Apply(ctx, &sts)

		if errors.Is(err, context.Canceled) {
			return err
		}
	}

	// DaemonSets
	daemonsets, err := client.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Printf("  - \033[31mERROR\033[0m Cannot list daemonsets: %s\n", err)
		return nil
	}

	for _, ds := range daemonsets.Items {
		fmt.Printf("  - Inspecting DaemonSet: %s\n", ds.Name)
		_, excluded := excludedApps[ds.Name]
		if excluded {
			fmt.Printf("    - Skipping daemonset due to exclusion file\n")
			continue
		}

		if dryRun {
			fmt.Printf("    - Dry-Run mode ENABLED - No changes will be made\n")
			continue
		}

		err = orchestrator.Apply(ctx, &ds)

		if errors.Is(err, context.Canceled) {
			return err
		}
	}

	// CronJobs - handle both v1 and v1beta1
	ver := cmdcontext.K8SVersionFromContext(ctx)
	if ver.LessThan(version.MustParseSemantic("1.21.0")) {
		// Use v1beta1 for Kubernetes < 1.21
		cronjobs, err := client.BatchV1beta1().CronJobs(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Printf("  - \033[31mERROR\033[0m Cannot list cronjobs (v1beta1): %s\n", err)
			return nil
		}

		for _, cj := range cronjobs.Items {
			fmt.Printf("  - Inspecting CronJob (v1beta1): %s\n", cj.Name)
			_, excluded := excludedApps[cj.Name]
			if excluded {
				fmt.Printf("    - Skipping cronjob due to exclusion file\n")
				continue
			}

			if dryRun {
				fmt.Printf("    - Dry-Run mode ENABLED - No changes will be made\n")
				continue
			}

			err = orchestrator.Apply(ctx, &cj)

			if errors.Is(err, context.Canceled) {
				return err
			}
		}
	} else {
		// Use v1 for Kubernetes >= 1.21
		cronjobs, err := client.BatchV1().CronJobs(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Printf("  - \033[31mERROR\033[0m Cannot list cronjobs (v1): %s\n", err)
			return nil
		}

		for _, cj := range cronjobs.Items {
			fmt.Printf("  - Inspecting CronJob (v1): %s\n", cj.Name)
			_, excluded := excludedApps[cj.Name]
			if excluded {
				fmt.Printf("    - Skipping cronjob due to exclusion file\n")
				continue
			}

			if dryRun {
				fmt.Printf("    - Dry-Run mode ENABLED - No changes will be made\n")
				continue
			}

			err = orchestrator.Apply(ctx, &cj)

			if errors.Is(err, context.Canceled) {
				return err
			}
		}
	}

	return nil
}

func sliceToMap(slice []string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, s := range slice {
		m[s] = struct{}{}
	}
	return m
}

// readAppListFromFile reads a list of apps from a file and returns a map of app names to struct{}
// the format of each line in the file can be either:
// <kind>/<name>: Exclude a specific app in the given namespace
// <namespace>/<kind>/<name>: Exclude a specific app in the given namespace
// <name>: Exclude any app of the given name in any namespace and kind
func readAppListFromFile(filename string) (map[string]interface{}, error) {
	apps := make(map[string]interface{})
	if filename == "" {
		return apps, nil
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(content), "\n")
	for lineNum, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Split and check format based on the number of parts
		parts := strings.Split(line, "/")

		if len(parts) > 3 {
			return nil, fmt.Errorf("invalid format on line %d (expected <kind>/<name> or <namespace>/<kind>/<name>): %s", lineNum+1, line)
		}

		if len(parts) == 1 {
			// Format: <name>
			apps[line] = struct{}{}
			continue
		}

		var namespaceStr, kindStr, nameStr string
		switch len(parts) {
		case 2:
			// Format: <kind>/<name>
			kindStr = parts[0]
			nameStr = parts[1]
		case 3:
			// Format: <namespace>/<kind>/<name>
			// add a slash to the namespace for the key in the map now so we don't need an extra check for namespace when building the key
			namespaceStr = parts[0] + "/"
			kindStr = parts[1]
			nameStr = parts[2]
		default:
			return nil, fmt.Errorf("invalid format on line %d (expected <kind>/<name> or <namespace>/<kind>/<name>): %s", lineNum+1, line)
		}

		// Validate and normalize the kind
		kind := workload.WorkloadKindFromString(kindStr)
		if kind == "" {
			return nil, fmt.Errorf("invalid workload kind '%s' on line %d: %s", kindStr, lineNum+1, line)
		}
		if !workload.IsValidWorkloadKind(kind) {
			return nil, fmt.Errorf("unsupported workload kind '%s' on line %d: %s", kindStr, lineNum+1, line)
		}

		// Normalize to lowercase
		normalizedKind := workload.WorkloadKindLowerCaseFromKind(kind)
		normalizedLine := fmt.Sprintf("%s%s/%s", namespaceStr, normalizedKind, nameStr)
		apps[normalizedLine] = struct{}{}
	}
	return apps, nil
}

func runPreflightChecks(ctx context.Context, cmd *cobra.Command, client *kube.Client, remote bool) {
	shouldSkip := cmd.Flag(skipPreflightChecksFlagName).Changed && cmd.Flag(skipPreflightChecksFlagName).Value.String() == "true"
	if shouldSkip {
		fmt.Printf("Skipping preflight checks due to --%s flag\n", skipPreflightChecksFlagName)
		return
	}

	fmt.Printf("Running preflight checks:\n")
	for _, check := range preflight.AllChecks {
		fmt.Printf("  - %-60s", check.Description())
		if err := check.Execute(client, ctx, remote); err != nil {
			fmt.Printf("\u001B[31mERROR\u001B[0m\n\n")
			fmt.Printf("Check failed: %s\n", err)
			os.Exit(1)
		} else {
			fmt.Printf("\u001B[32mPASS\u001B[0m\n")
		}
	}

	fmt.Printf("  - All preflight checks passed!\n\n")
}
