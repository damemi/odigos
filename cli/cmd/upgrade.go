/*
Copyright © 2023 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"
	"os"

	"github.com/hashicorp/go-version"
	"github.com/keyval-dev/odigos/cli/cmd/resources"
	"github.com/keyval-dev/odigos/cli/cmd/resources/odigospro"
	"github.com/keyval-dev/odigos/cli/pkg/confirm"
	"github.com/keyval-dev/odigos/cli/pkg/kube"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type VersionChangeType int

// upgradeCmd represents the upgrade command
var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade Odigos version",
	Long: `Upgrade odigos version in your cluster.

This command will upgrade the Odigos version in the cluster to the version of Odigos CLI
and apply any required migrations and adaptations.`,
	Run: func(cmd *cobra.Command, args []string) {

		client, err := kube.CreateClient(cmd)
		if err != nil {
			kube.PrintClientErrorAndExit(err)
		}
		ctx := cmd.Context()

		ns, err := resources.GetOdigosNamespace(client, ctx)
		if err != nil {
			fmt.Println("No Odigos installation found in cluster to upgrade")
			os.Exit(1)
		}

		cm, err := client.CoreV1().ConfigMaps(ns).Get(ctx, resources.OdigosDeploymentConfigMapName, metav1.GetOptions{})
		if err != nil {
			fmt.Println("Odigos upgrade failed - unable to read the current Odigos version for migration")
			os.Exit(1)
		}

		currOdigosVersion := cm.Data["ODIGOS_VERSION"]
		if currOdigosVersion == "" {
			fmt.Println("Odigos upgrade failed - unable to read the current Odigos version for migration")
			os.Exit(1)
		}

		sourceVersion, err := version.NewVersion(currOdigosVersion)
		if err != nil {
			fmt.Println("Odigos upgrade failed - unable to parse the current Odigos version for migration")
			os.Exit(1)
		}
		if sourceVersion.LessThan(version.Must(version.NewVersion("1.0.0"))) {
			fmt.Printf("Unable to upgrade from Odigos version older than 'v1.0.0' current version is %s.\n", currOdigosVersion)
			fmt.Printf("To upgrade, please use 'odigos uninstall' and 'odigos install'.\n")
			os.Exit(1)
		}
		targetVersion, err := version.NewVersion(versionFlag)
		if err != nil {
			fmt.Println("Odigos upgrade failed - unable to parse the target Odigos version for migration")
			os.Exit(1)
		}

		var operation string
		if sourceVersion.GreaterThan(targetVersion) {
			fmt.Printf("About to DOWNGRADE Odigos version from '%s' (current) to '%s' (target)\n", currOdigosVersion, versionFlag)
			operation = "Downgrading"
		} else {
			fmt.Printf("About to upgrade Odigos version from '%s' (current) to '%s' (target)\n", currOdigosVersion, versionFlag)
			operation = "Upgrading"
		}

		confirmed, err := confirm.Ask("Are you sure?")
		if err != nil || !confirmed {
			fmt.Println("Aborting upgrade")
			return
		}

		config, err := resources.GetCurrentConfig(ctx, client, ns)
		if err != nil {
			fmt.Println("Odigos upgrade failed - unable to read the current Odigos configuration.")
			os.Exit(1)
		}

		// update the config on upgrade
		config.Spec.OdigosVersion = versionFlag
		config.Spec.ConfigVersion += 1

		currentTier, err := odigospro.GetCurrentOdigosTier(ctx, client, ns)
		if err != nil {
			fmt.Println("Odigos cloud login failed - unable to read the current Odigos tier.")
			os.Exit(1)
		}
		resourceManagers := resources.CreateResourceManagers(client, ns, currentTier, nil, &config.Spec)
		err = resources.ApplyResourceManagers(ctx, client, resourceManagers, operation)
		if err != nil {
			fmt.Println("Odigos upgrade failed - unable to apply Odigos resources.")
			os.Exit(1)
		}
		err = resources.DeleteOldOdigosSystemObjects(ctx, client, ns, config)
		if err != nil {
			fmt.Println("Odigos upgrade failed - unable to cleanup old Odigos resources.")
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
	if OdigosVersion != "" {
		versionFlag = OdigosVersion
	} else {
		upgradeCmd.Flags().StringVar(&versionFlag, "version", OdigosVersion, "for development purposes only")
	}
}
