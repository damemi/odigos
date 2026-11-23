//go:build tools

// Package tools pins module dependencies for binaries built in the odiglet image
// but not imported by odiglet source (e.g. odigos-csi-driver, deviceplugin).
package tools

import (
	_ "github.com/container-storage-interface/spec/lib/go/csi"
	_ "github.com/odigos-io/odigos/deviceplugin/pkg"
	_ "k8s.io/kubelet/pkg/apis/pluginregistration/v1"
)
