//go:build tools

// Package tools pins module dependencies for binaries built in the odiglet image
// but not imported by enterprise odiglet source (e.g. odigos-csi-driver).
package tools

import (
	_ "github.com/container-storage-interface/spec/lib/go/csi"
	_ "k8s.io/kubelet/pkg/apis/pluginregistration/v1"
)
