package nodedetails

import (
	"context"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/features"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/odiglet/pkg/nodedetails"
	v1 "k8s.io/api/core/v1"
)

// WaspFeature checks if Wasp should be enabled on the node.
// Wasp is a solution that provides functionality when the bpf_probe_write_user eBPF helper
// is NOT available in the kernel.
type WaspFeature struct{}

func (w *WaspFeature) Name() string {
	return "Wasp"
}

func (w *WaspFeature) Check(ctx context.Context, node *v1.Node, spec *v1alpha1.NodeDetailsSpec) error {
	// Check if the bpf_probe_write_user helper is available in the kernel
	// Wasp is enabled as a fallback when the kernel does NOT support this helper natively
	supportsWriteUser := features.HaveProgramHelper(ebpf.Kprobe, asm.FnProbeWriteUser) == nil

	// Enable Wasp when the kernel feature is missing (inverted logic)
	spec.WaspEnabled = !supportsWriteUser

	return nil
}

func init() {
	// Register the enterprise Wasp feature
	// This init() function runs automatically when the package is imported,
	// adding the enterprise feature to the OSS feature registry
	nodedetails.RegisterFeature(&WaspFeature{})
}
