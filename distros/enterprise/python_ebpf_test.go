package distros

import (
	"testing"

	"github.com/odigos-io/odigos/distros/distro"
)

// python-ebpf is chosen per workload by an instrumentation rule; a Python version it does not support
// must keep today's agent rather than leave the container uninstrumented.
func TestPythonEbpfFallsBackToPythonEnterprise(t *testing.T) {
	p, err := NewEnterpriseProvider()
	if err != nil {
		t.Fatal(err)
	}
	d := p.GetDistroByName("python-ebpf")
	if d == nil {
		t.Fatal("python-ebpf missing from enterprise distros")
	}
	if d.FallbackDistro == nil || *d.FallbackDistro != "python-enterprise" {
		t.Fatalf("fallbackDistro = %v, want python-enterprise", d.FallbackDistro)
	}
	// No device and no env: the odiglet attaches to the running process, with no restart.
	if distro.IsRestartRequired(d, nil) {
		t.Fatal("python-ebpf must not require a restart")
	}
	if p.GetDefaultDistroNames()["python"] != "python-enterprise" {
		t.Fatal("python-ebpf must not become the default: it is chosen by the instrumentations-v2 rule")
	}

	for version, want := range map[string]string{
		"3.6.15":  "python-ebpf",
		"3.7.17":  "python-ebpf",
		"3.8.20":  "python-ebpf",
		"3.12.7":  "python-ebpf",
		"3.15.0":  "python-ebpf",
		"3.16.0":  "python-enterprise",
		"3.5.10":  "python-enterprise3.8", // end of the chain: the resolver reports it unsupported
		"":        "python-ebpf",          // unknown version: try python-ebpf, which checks the binary
		"unknown": "python-ebpf",
	} {
		if got := p.ResolveDistroNameForVersion("python-ebpf", version); got != want {
			t.Errorf("version %q resolves to %s, want %s", version, got, want)
		}
	}
}
