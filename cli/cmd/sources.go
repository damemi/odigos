package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/cli/cmd/sources_utils"
	cmdcontext "github.com/odigos-io/odigos/cli/pkg/cmd_context"
	"github.com/odigos-io/odigos/cli/pkg/confirm"
	"github.com/odigos-io/odigos/cli/pkg/kube"
	"github.com/odigos-io/odigos/cli/pkg/lifecycle"
	"github.com/odigos-io/odigos/cli/pkg/preflight"
	"github.com/odigos-io/odigos/cli/pkg/remote"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/version"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	sourceFlags *pflag.FlagSet

	namespaceFlagName   = "namespace"
	sourceNamespaceFlag string

	allNamespacesFlagName = "all-namespaces"
	allNamespaceFlag      bool

	workloadKindFlagName = "workload-kind"
	workloadKindFlag     string

	workloadNameFlagName = "workload-name"
	workloadNameFlag     string

	workloadNamespaceFlagName = "workload-namespace"
	workloadNamespaceFlag     string

	disableInstrumentationFlagName = "disable-instrumentation"
	disableInstrumentationFlag     bool

	sourceGroupFlagName = "group"
	sourceGroupFlag     string

	sourceSetGroupFlagName = "set-group"
	sourceSetGroupFlag     string

	sourceRemoveGroupFlagName = "remove-group"
	sourceRemoveGroupFlag     string

	sourceOtelServiceFlagName = "otel-service"
	sourceOtelServiceFlag     string

	excludeAppsFileFlagName = "exclude-apps-file"
	excludeAppsFileFlag     string

	excludeNamespacesFileFlagName = "exclude-namespaces-file"
	excludeNamespacesFileFlag     string

	dryRunFlagName = "dry-run"
	dryRunFlag     bool

	remoteFlagName = "remote"
	remoteFlag     bool

	instrumentationCoolOffFlagName = "instrumentation-cool-off"

	onlyDeploymentFlagName = "only-deployment"
	onlyNamespaceFlagName  = "only-namespace"

	skipPreflightChecksFlagName = "skip-preflight-checks"
	skipPreflightChecksFlag     bool
)

var sourcesCmd = &cobra.Command{
	Use:   "sources [command] [flags]",
	Short: "Manage Odigos Sources in a cluster",
	Long:  "This command can be used to create, delete, or update Sources to configure workload or namespace auto-instrumentation",
	Example: `# Create a Source "foo-source" for deployment "foo" in namespace "default"
odigos sources create foo-source --workload-kind=Deployment --workload-name=foo --workload-namespace=default -n default

# Update all existing Sources in namespace "default" to disable instrumentation
odigos sources update --disable-instrumentation -n default

# Delete all Sources in group "mygroup"
odigos sources delete --group mygroup --all-namespaces
	`,
}

var kindAliases = map[k8sconsts.WorkloadKind][]string{
	k8sconsts.WorkloadKindDeployment:  []string{"deploy", "deployments", "deploy.apps", "deployment.apps", "deployments.apps"},
	k8sconsts.WorkloadKindDaemonSet:   []string{"ds", "daemonsets", "ds.apps", "daemonset.apps", "daemonsets.apps"},
	k8sconsts.WorkloadKindStatefulSet: []string{"sts", "statefulsets", "sts.apps", "statefulset.apps", "statefulsets.apps"},
	k8sconsts.WorkloadKindNamespace:   []string{"ns", "namespaces"},
}

var sourceDisableCmd = &cobra.Command{
	Use:     "disable [workload type] [workload name] [flags]",
	Short:   "Disable a source for Odigos instrumentation.",
	Long:    "This command disables the given workload for Odigos instrumentation. It will create a Source object (if one does not already exist)",
	Aliases: []string{"uninstrument"},
	Example: `
# Disable deployment "foo" in namespace "default"
odigos sources disable deployment foo

# Disable namespace "bar"
odigos sources disable namespace bar

# Disable statefulset "foo" in namespace "bar"
odigos sources disable statefulset foo -n bar
`,
}

var sourceEnableCmd = &cobra.Command{
	Use:     "enable [workload type] [workload name] [flags]",
	Short:   "Enable a source for Odigos instrumentation.",
	Long:    "This command enables the given workload for Odigos instrumentation. It will create a Source object (if one does not already exist)",
	Aliases: []string{"instrument"},
	Example: `
# Enable deployment "foo" in namespace "default"
odigos sources enable deployment foo

# Enable namespace "bar"
odigos sources enable namespace bar

# Enable statefulset "foo" in namespace "bar"
odigos sources enable statefulset foo -n bar
`,
}

var sourceCreateCmd = &cobra.Command{
	Use:   "create [name] [flags]",
	Short: "Create an Odigos Source",
	Long:  "This command will create the named Source object for the provided workload.",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := cmd.Context()
		client := cmdcontext.KubeClientFromContextOrExit(ctx)
		disableInstrumentation := disableInstrumentationFlag
		sourceName := args[0]

		source := &v1alpha1.Source{
			ObjectMeta: v1.ObjectMeta{
				Name:      sourceName,
				Namespace: sourceNamespaceFlag,
			},
			Spec: v1alpha1.SourceSpec{
				Workload: k8sconsts.PodWorkload{
					Kind:      k8sconsts.WorkloadKind(workloadKindFlag),
					Name:      workloadNameFlag,
					Namespace: workloadNamespaceFlag,
				},
				DisableInstrumentation: disableInstrumentation,
				OtelServiceName:        sourceOtelServiceFlag,
			},
		}

		if len(sourceGroupFlag) > 0 {
			source.Labels = make(map[string]string)
			source.Labels[k8sconsts.SourceDataStreamLabelPrefix+sourceGroupFlag] = "true"
		}

		_, err := client.OdigosClient.Sources(sourceNamespaceFlag).Create(ctx, source, v1.CreateOptions{})
		if err != nil {
			fmt.Printf("\033[31mERROR\033[0m Cannot create Source: %+v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created Source %s\n", sourceName)
	},
}

var sourceDeleteCmd = &cobra.Command{
	Use:   "delete [name] [flags]",
	Short: "Delete Odigos Sources",
	Long: `This command will delete the named Source object or any Source objects that match the provided Workload info.
If a [name] is provided, that Source object will be deleted in the given namespace using the --namespace (-n) flag.`,
	Example: `For example, to delete the Source named "mysource-abc123" in namespace "myapp", run:

$ odigos sources delete mysource-abc123 -n myapp

Multiple Source objects can be deleted at once using the --workload-name, --workload-kind, and --workload-namespace flags.
These flags are AND-ed so that if any of these flags are provided, all Sources that match the given flags will be deleted.

For example, to delete all Sources for StatefulSet workloads in the cluster, run:

$ odigos sources delete --workload-kind=StatefulSet --all-namespaces

To delete all Deployment Sources in namespace Foobar, run:

$ odigos sources delete --workload-kind=Deployment --workload-namespace=Foobar

or

$ odigos sources delete --workload-kind=Deployment -n Foobar

These flags can be used to batch delete Sources, or as an alternative to deleting a Source by name (for instance, when
the name of the Source might not be known, but the Workload information is known). For example:

$ odigos sources delete --workload-kind=Deployment --workload-name=myapp -n myapp-namespace

This command will delete any Sources in the namespace "myapp-namespace" that instrument a Deployment named "myapp"

It is important to note that if a Source [name] is provided, all --workload-* flags will be ignored to delete only the named Source.
`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := cmd.Context()
		client := cmdcontext.KubeClientFromContextOrExit(ctx)

		if len(args) > 0 {
			sourceName := args[0]
			fmt.Printf("Deleting Source %s in namespace %s\n", sourceName, sourceNamespaceFlag)
			err := client.OdigosClient.Sources(sourceNamespaceFlag).Delete(ctx, sourceName, v1.DeleteOptions{})
			if err != nil {
				fmt.Printf("\033[31mERROR\033[0m Cannot delete source %s in namespace %s: %+v\n", sourceName, sourceNamespaceFlag, err)
				os.Exit(1)
			} else {
				fmt.Printf("Deleted source %s in namespace %s\n", sourceName, sourceNamespaceFlag)
			}
		} else {
			namespaceText, providedWorkloadFlags, namespaceList, labelSet := parseSourceLabelFlags()

			if !cmd.Flag("yes").Changed {
				fmt.Printf("About to delete all Sources in %s that match:\n%s", namespaceText, providedWorkloadFlags)
				confirmed, err := confirm.Ask("Are you sure?")
				if err != nil || !confirmed {
					fmt.Println("Aborting delete")
					return
				}
			}

			sources, err := client.OdigosClient.Sources(namespaceList).List(ctx, v1.ListOptions{LabelSelector: labelSet.AsSelector().String()})
			if err != nil {
				fmt.Printf("\033[31mERROR\033[0m Cannot list Sources: %+v\n", err)
				os.Exit(1)
			}

			deletedCount := 0
			for _, source := range sources.Items {
				err := client.OdigosClient.Sources(source.GetNamespace()).Delete(ctx, source.GetName(), v1.DeleteOptions{})
				if err != nil {
					fmt.Printf("\033[31mERROR\033[0m Cannot delete Sources %s/%s: %+v\n", source.GetNamespace(), source.GetName(), err)
					os.Exit(1)
				}
				fmt.Printf("Deleted Source %s/%s\n", source.GetNamespace(), source.GetName())
				deletedCount++
			}
			fmt.Printf("Deleted %d Sources\n", deletedCount)
		}
	},
}

var sourceUpdateCmd = &cobra.Command{
	Use:   "update [name] [flags]",
	Short: "Update Odigos Sources",
	Long: `This command will update the named Source object or any Source objects that match the provided Workload info.
If a [name] is provided, that Source object will be updated in the given namespace using the --namespace (-n) flag.`,
	Example: `For example, to update the Source named "mysource-abc123" in namespace "myapp", run:

$ odigos sources update mysource-abc123 -n myapp <flags>

Multiple Source objects can be updated at once using the --workload-name, --workload-kind, and --workload-namespace flags.
These flags are AND-ed so that if any of these flags are provided, all Sources that match the given flags will be updated.

For example, to update all Sources for StatefulSet workloads in the cluster, run:

$ odigos sources update --workload-kind=StatefulSet --all-namespaces <flags>

To update all Deployment Sources in namespace Foobar, run:

$ odigos sources update --workload-kind=Deployment --workload-namespace=Foobar <flags>

or

$ odigos sources update --workload-kind=Deployment -n Foobar <flags>

These flags can be used to batch update Sources, or as an alternative to updating a Source by name (for instance, when
the name of the Source might not be known, but the Workload information is known). For example:

$ odigos sources update --workload-kind=Deployment --workload-name=myapp -n myapp-namespace <flags>

This command will update any Sources in the namespace "myapp-namespace" that instrument a Deployment named "myapp"

It is important to note that if a Source [name] is provided, all --workload-* flags will be ignored to update only the named Source.
`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := cmd.Context()
		client := cmdcontext.KubeClientFromContextOrExit(ctx)

		sourceList := &v1alpha1.SourceList{}
		if len(args) > 0 {
			sourceName := args[0]
			sources, err := client.OdigosClient.Sources(sourceNamespaceFlag).List(ctx, v1.ListOptions{FieldSelector: "metadata.name=" + sourceName})
			if err != nil {
				fmt.Printf("\033[31mERROR\033[0m Cannot list Source %s: %+v\n", sourceName, err)
				os.Exit(1)
			}
			sourceList = sources
		} else {
			namespaceText, providedWorkloadFlags, namespaceList, labelSet := parseSourceLabelFlags()

			if !cmd.Flag("yes").Changed {
				fmt.Printf("About to update all Sources in %s that match:\n%s", namespaceText, providedWorkloadFlags)
				confirmed, err := confirm.Ask("Are you sure?")
				if err != nil || !confirmed {
					fmt.Println("Aborting update")
					return
				}
			}

			sources, err := client.OdigosClient.Sources(namespaceList).List(ctx, v1.ListOptions{LabelSelector: labelSet.AsSelector().String()})
			if err != nil {
				fmt.Printf("\033[31mERROR\033[0m Cannot list Sources: %+v\n", err)
				os.Exit(1)
			}
			sourceList = sources
		}

		for _, source := range sourceList.Items {
			source.Spec.DisableInstrumentation = disableInstrumentationFlag
			if len(sourceRemoveGroupFlag) > 0 {
				for label, value := range source.Labels {
					if label == k8sconsts.SourceDataStreamLabelPrefix+sourceRemoveGroupFlag && value == "true" {
						delete(source.Labels, k8sconsts.SourceDataStreamLabelPrefix+sourceRemoveGroupFlag)
					}
				}
			}
			if len(sourceSetGroupFlag) > 0 {
				if source.Labels == nil {
					source.Labels = make(map[string]string)
				}
				source.Labels[k8sconsts.SourceDataStreamLabelPrefix+sourceSetGroupFlag] = "true"
			}

			if len(sourceOtelServiceFlag) > 0 {
				source.Spec.OtelServiceName = sourceOtelServiceFlag
			}

			_, err := client.OdigosClient.Sources(source.GetNamespace()).Update(ctx, &source, v1.UpdateOptions{})
			if err != nil {
				fmt.Printf("\033[31mERROR\033[0m Cannot update Sources %s/%s: %+v\n", source.GetNamespace(), source.GetName(), err)
				os.Exit(1)
			}
			fmt.Printf("Updated Source %s/%s\n", source.GetNamespace(), source.GetName())
		}
	},
}

var errorOnly bool

var sourceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the status of all Odigos Sources",
	Long:  "Lists all InstrumentationConfigs and InstrumentationInstances with their current status. Use --error to filter only failed sources.",
	Run: func(cmd *cobra.Command, args []string) {
		ctx := cmd.Context()

		statuses, err := sources_utils.SourcesStatus(ctx)
		if err != nil {
			fmt.Printf("\033[31mERROR\033[0m Failed to retrieve source statuses: %+v\n", err)
			return
		}

		if errorOnly {
			var filteredStatuses []sources_utils.SourceStatus
			for _, s := range statuses {
				if s.IsError {
					filteredStatuses = append(filteredStatuses, s)
				}
			}
			statuses = filteredStatuses
		}

		fmt.Println("\n\033[33mOdigos Source Status:\033[0m")
		w := tabwriter.NewWriter(os.Stdout, 20, 4, 2, ' ', tabwriter.TabIndent)

		fmt.Fprintln(w, "NAMESPACE\tNAME\tSTATUS\tMESSAGE")

		for _, status := range statuses {
			color := "\033[32m"
			if status.IsError {
				color = "\033[31m"
			}

			fmt.Fprintf(w, "%s%s\t%s\t%s\t%s\033[0m\n",
				color, status.Namespace, status.Name, status.Status, status.Message)
		}

		w.Flush()
	},
}

func enableOrDisableSource(cmd *cobra.Command, args []string, workloadKind k8sconsts.WorkloadKind, disableInstrumentation bool, namespace string) {
	msg := "enable"
	if disableInstrumentation {
		msg = "disable"
	}

	ctx := cmd.Context()
	client := cmdcontext.KubeClientFromContextOrExit(ctx)
	source, err := updateOrCreateSourceForObject(ctx, client, workloadKind, args[0], disableInstrumentation, namespace)
	if err != nil {
		fmt.Printf("\033[31mERROR\033[0m Cannot %s Source: %+v\n", msg, err)
		os.Exit(1)
	}
	dryRunMsg := ""
	if dryRunFlag {
		dryRunMsg = "\033[31m(dry run)\033[0m"
	}
	fmt.Printf("%s%sd Source %s for %s %s (disabled=%t)\n", dryRunMsg, msg, source.GetName(), source.Spec.Workload.Kind, source.Spec.Workload.Name, disableInstrumentation)
}

func enableOrDisableSourceCmd(workloadKind k8sconsts.WorkloadKind, disableInstrumentation bool) *cobra.Command {
	msg := "enable"
	if disableInstrumentation {
		msg = "disable"
	}

	return &cobra.Command{
		Use:     fmt.Sprintf("%s [name]", workload.WorkloadKindLowerCaseFromKind(workloadKind)),
		Short:   fmt.Sprintf("%s a %s for Odigos instrumentation", msg, workloadKind),
		Long:    fmt.Sprintf("This command %ss the provided %s for Odigos instrumentatin. It will create a Source object if one does not already exists, or update the existing one if it does.", msg, workloadKind),
		Args:    cobra.ExactArgs(1),
		Aliases: kindAliases[workloadKind],
		Run: func(cmd *cobra.Command, args []string) {
			enableOrDisableSource(cmd, args, workloadKind, disableInstrumentation, sourceNamespaceFlag)
		},
	}
}

func enableClusterSourceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cluster",
		Short: "Enable an entire cluster for Odigos instrumentation",
		Long:  "This command enables the cluster for Odigos instrumentation. It will create Source objects for all apps in the cluster, except those that are excluded.",
		Example: `
# Enable the cluster for Odigos instrumentation
odigos sources enable cluster

# Enable the cluster for Odigos instrumentation, but dry run
odigos sources enable cluster --dry-run

# Enable the cluster for Odigos instrumentation with excluded namespaces
odigos sources enable cluster --exclude-namespaces-file=excluded-namespaces.txt

# Enable the cluster for Odigos instrumentation with excluded apps
odigos sources enable cluster --exclude-apps-file=excluded-apps.txt

# Enable the cluster for Odigos instrumentation with excluded namespaces and apps
odigos sources enable cluster --exclude-namespaces-file=excluded-namespaces.txt --exclude-apps-file=excluded-apps.txt

For example, excluded-namespaces.txt:
namespace1
namespace2

For example, excluded-apps.txt:
app1
app2
`,
		Run: func(cmd *cobra.Command, args []string) {
			enableClusterSource(cmd, args)
		},
	}
}

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

func updateOrCreateSourceForObject(ctx context.Context, client *kube.Client, workloadKind k8sconsts.WorkloadKind, argName string, disableInstrumentation bool, namespace string) (*v1alpha1.Source, error) {
	var err error
	obj := workload.ClientObjectFromWorkloadKind(workloadKind)
	var objName, objNamespace, sourceNamespace string
	switch workloadKind {
	case k8sconsts.WorkloadKindDaemonSet:
		obj, err = client.Clientset.AppsV1().DaemonSets(namespace).Get(ctx, argName, metav1.GetOptions{})
		objName = obj.GetName()
		objNamespace = obj.GetNamespace()
		sourceNamespace = namespace
	case k8sconsts.WorkloadKindDeployment:
		obj, err = client.Clientset.AppsV1().Deployments(namespace).Get(ctx, argName, metav1.GetOptions{})
		objName = obj.GetName()
		objNamespace = obj.GetNamespace()
		sourceNamespace = namespace
	case k8sconsts.WorkloadKindStatefulSet:
		obj, err = client.Clientset.AppsV1().StatefulSets(namespace).Get(ctx, argName, metav1.GetOptions{})
		objName = obj.GetName()
		objNamespace = obj.GetNamespace()
		sourceNamespace = namespace
	case k8sconsts.WorkloadKindNamespace:
		obj, err = client.Clientset.CoreV1().Namespaces().Get(ctx, argName, metav1.GetOptions{})
		objName = obj.GetName()
		objNamespace = obj.GetName()
		sourceNamespace = obj.GetName()
	case k8sconsts.WorkloadKindCronJob:
		ver := cmdcontext.K8SVersionFromContext(ctx)
		if ver.LessThan(version.MustParseSemantic("1.21.0")) {
			obj, err = client.Clientset.BatchV1beta1().CronJobs(namespace).Get(ctx, argName, metav1.GetOptions{})
		} else {
			obj, err = client.Clientset.BatchV1().CronJobs(namespace).Get(ctx, argName, metav1.GetOptions{})
		}
		objName = obj.GetName()
		objNamespace = obj.GetNamespace()
		sourceNamespace = namespace
	}
	if err != nil {
		return nil, err
	}

	var source *v1alpha1.Source
	selector := labels.SelectorFromSet(labels.Set{
		k8sconsts.WorkloadNameLabel:      obj.GetName(),
		k8sconsts.WorkloadNamespaceLabel: sourceNamespace,
		k8sconsts.WorkloadKindLabel:      string(workloadKind),
	})
	sources, err := client.OdigosClient.Sources(sourceNamespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if len(sources.Items) > 0 {
		source = &sources.Items[0]
		if source.Spec.DisableInstrumentation == disableInstrumentation {
			fmt.Printf("NOTE: Source %s unchanged.\n", source.Name)
			return source, nil
		}
	} else {
		source = &v1alpha1.Source{
			ObjectMeta: v1.ObjectMeta{
				GenerateName: workload.CalculateWorkloadRuntimeObjectName(objName, workloadKind),
				Namespace:    sourceNamespace,
			},
			Spec: v1alpha1.SourceSpec{
				Workload: k8sconsts.PodWorkload{
					Kind:      workloadKind,
					Name:      objName,
					Namespace: objNamespace,
				},
			},
		}

	}

	source.Spec.DisableInstrumentation = disableInstrumentation

	if !dryRunFlag {
		if len(sources.Items) > 0 {
			source, err = client.OdigosClient.Sources(sourceNamespace).Update(ctx, source, v1.UpdateOptions{})
		} else {
			source, err = client.OdigosClient.Sources(sourceNamespace).Create(ctx, source, v1.CreateOptions{})
		}
		if err != nil {
			return nil, err
		}
	}

	if workloadKind == k8sconsts.WorkloadKindNamespace {
		// if toggling a namespace, check for individually instrumented workloads
		// alert the user that these workloads won't be affected by the command
		selector := fmt.Sprintf("%s != %s", k8sconsts.WorkloadKindLabel, k8sconsts.WorkloadKindNamespace)
		sources, err := client.OdigosClient.Sources(sourceNamespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return source, err
		}
		if len(sources.Items) > 0 {
			sourceList := make([]string, 0)
			for _, source := range sources.Items {
				if source.Spec.DisableInstrumentation != disableInstrumentation {
					sourceList = append(sourceList, fmt.Sprintf("Source: %s (Workload=%s, Kind=%s, disabled=%t)\n", source.GetName(), source.Spec.Workload.Name, source.Spec.Workload.Kind, source.Spec.DisableInstrumentation))
				}
			}
			if len(sourceList) > 0 {
				fmt.Printf("NOTE: Configured Namespace Source, but the following Workload Sources will not be affected (individual Workload Sources take priority over Namespace Sources):\n")
				for _, line := range sourceList {
					fmt.Printf(line)
				}
			}
		}
	} else {
		// if toggling a workload, check if there is a namespace source and alert the user of that
		selector := fmt.Sprintf("%s = %s", k8sconsts.WorkloadKindLabel, k8sconsts.WorkloadKindNamespace)
		sources, err := client.OdigosClient.Sources(sourceNamespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return source, err
		}
		if len(sources.Items) > 0 {
			for _, source := range sources.Items {
				if source.Spec.DisableInstrumentation != disableInstrumentation {
					fmt.Printf("NOTE: Workload Source configuration (disabled=%t) is different from Namespace Source %s (disabled=%t). Workload Source will take priority.\n", disableInstrumentation, source.GetName(), source.Spec.DisableInstrumentation)
				}
			}
		}
	}
	return source, nil
}

func parseSourceLabelFlags() (string, string, string, labels.Set) {
	labelSet := labels.Set{}
	providedWorkloadFlags := ""
	if len(workloadKindFlag) > 0 {
		providedWorkloadFlags = fmt.Sprintf("%s Workload Kind: %s\n", providedWorkloadFlags, workloadKindFlag)
		labelSet[k8sconsts.WorkloadKindLabel] = workloadKindFlag
	}
	if len(workloadNameFlag) > 0 {
		providedWorkloadFlags = fmt.Sprintf("%s Workload Name: %s\n", providedWorkloadFlags, workloadNameFlag)
		labelSet[k8sconsts.WorkloadNameLabel] = workloadNameFlag
	}
	if len(workloadNamespaceFlag) > 0 {
		providedWorkloadFlags = fmt.Sprintf("%s Workload Namespace: %s\n", providedWorkloadFlags, workloadNamespaceFlag)
		labelSet[k8sconsts.WorkloadNamespaceLabel] = workloadNamespaceFlag
	}
	if len(sourceGroupFlag) > 0 {
		providedWorkloadFlags = fmt.Sprintf("%s Source Group: %s\n", providedWorkloadFlags, sourceGroupFlag)
		labelSet[k8sconsts.SourceDataStreamLabelPrefix+sourceGroupFlag] = "true"
	}
	namespaceList := sourceNamespaceFlag
	namespaceText := fmt.Sprintf("namespace %s", sourceNamespaceFlag)
	if allNamespaceFlag {
		namespaceText = "all namespaces"
		namespaceList = ""
	}
	return namespaceText, providedWorkloadFlags, namespaceList, labelSet
}

func init() {
	sourceFlags = pflag.NewFlagSet("sourceFlags", pflag.ContinueOnError)
	sourceFlags.StringVarP(&sourceNamespaceFlag, namespaceFlagName, "n", "default", "Kubernetes Namespace for Source")
	sourceFlags.StringVar(&workloadKindFlag, workloadKindFlagName, "", "Kubernetes Kind for entity (one of: Deployment, DaemonSet, StatefulSet, Namespace)")
	sourceFlags.StringVar(&workloadNameFlag, workloadNameFlagName, "", "Name of entity for Source")
	sourceFlags.StringVar(&workloadNamespaceFlag, workloadNamespaceFlagName, "", "Namespace of entity for Source")
	sourceFlags.StringVar(&sourceGroupFlag, sourceGroupFlagName, "", "Name of Source group to use")

	rootCmd.AddCommand(sourcesCmd)
	sourcesCmd.AddCommand(sourceCreateCmd)
	sourcesCmd.AddCommand(sourceDeleteCmd)
	sourcesCmd.AddCommand(sourceUpdateCmd)
	sourcesCmd.AddCommand(sourceStatusCmd)

	sourcesCmd.AddCommand(sourceEnableCmd)
	sourcesCmd.AddCommand(sourceDisableCmd)

	for _, kind := range []k8sconsts.WorkloadKind{
		k8sconsts.WorkloadKindDeployment,
		k8sconsts.WorkloadKindDaemonSet,
		k8sconsts.WorkloadKindStatefulSet,
		k8sconsts.WorkloadKindNamespace,
		k8sconsts.WorkloadKindCronJob,
	} {
		enableCmd := enableOrDisableSourceCmd(kind, false)
		disableCmd := enableOrDisableSourceCmd(kind, true)
		if kind != k8sconsts.WorkloadKindNamespace {
			enableCmd.Flags().StringVarP(&sourceNamespaceFlag, namespaceFlagName, "n", "default", "Kubernetes Namespace for Source")
			disableCmd.Flags().StringVarP(&sourceNamespaceFlag, namespaceFlagName, "n", "default", "Kubernetes Namespace for Source")
		}
		enableCmd.Flags().Bool(dryRunFlagName, false, "dry run")
		disableCmd.Flags().Bool(dryRunFlagName, false, "dry run")
		sourceEnableCmd.AddCommand(enableCmd)
		sourceDisableCmd.AddCommand(disableCmd)
	}

	enableClusterSourceCmd := enableClusterSourceCmd()
	enableClusterSourceCmd.Flags().StringVar(&excludeAppsFileFlag, excludeAppsFileFlagName, "", "Path to file containing apps to exclude")
	enableClusterSourceCmd.Flags().StringVar(&excludeNamespacesFileFlag, excludeNamespacesFileFlagName, "", "Path to file containing namespaces to exclude")
	enableClusterSourceCmd.Flags().BoolVar(&dryRunFlag, dryRunFlagName, false, "dry run")
	enableClusterSourceCmd.Flags().BoolVar(&skipExcludedNamespacesFlag, skipExcludedNamespacesFlagName, false, "Passively ignore excluded namespaces instead of creating disabled sources for them")
	enableClusterSourceCmd.Flags().BoolVar(&remoteFlag, remoteFlagName, false, "remote")
	enableClusterSourceCmd.Flags().Duration(instrumentationCoolOffFlagName, 0, "Cool-off period for instrumentation. Time format is 1h30m")
	enableClusterSourceCmd.Flags().String(onlyNamespaceFlagName, "", "Namespace of the deployment to instrument (must be used with --only-deployment)")
	enableClusterSourceCmd.Flags().String(onlyDeploymentFlagName, "", "Name of the deployment to instrument (must be used with --only-namespace)")
	enableClusterSourceCmd.Flags().Bool(skipPreflightChecksFlagName, false, "Skip preflight checks")
	sourceEnableCmd.AddCommand(enableClusterSourceCmd)

	sourceCreateCmd.Flags().AddFlagSet(sourceFlags)
	sourceCreateCmd.Flags().BoolVar(&disableInstrumentationFlag, disableInstrumentationFlagName, false, "Disable instrumentation for Source")
	sourceCreateCmd.Flags().StringVar(&sourceOtelServiceFlag, sourceOtelServiceFlagName, "", "OpenTelemetry service name to use for the Source")

	sourceDeleteCmd.Flags().AddFlagSet(sourceFlags)
	sourceDeleteCmd.Flags().Bool("yes", false, "skip the confirmation prompt")
	sourceDeleteCmd.Flags().Bool(allNamespacesFlagName, false, "apply to all Kubernetes namespaces")

	sourceUpdateCmd.Flags().AddFlagSet(sourceFlags)
	sourceUpdateCmd.Flags().BoolVar(&disableInstrumentationFlag, disableInstrumentationFlagName, false, "Disable instrumentation for Source")
	sourceUpdateCmd.Flags().StringVar(&sourceSetGroupFlag, sourceSetGroupFlagName, "", "Group name to be applied to the Source")
	sourceUpdateCmd.Flags().StringVar(&sourceRemoveGroupFlag, sourceRemoveGroupFlagName, "", "Group name to be removed from the Source (if set)")
	sourceUpdateCmd.Flags().Bool("yes", false, "skip the confirmation prompt")
	sourceUpdateCmd.Flags().Bool(allNamespacesFlagName, false, "apply to all Kubernetes namespaces")
	sourceUpdateCmd.Flags().StringVar(&sourceOtelServiceFlag, sourceOtelServiceFlagName, "", "OpenTelemetry service name to use for the Source")

	sourceStatusCmd.Flags().BoolVar(&errorOnly, "error", false, "Show only sources with errors")

}
