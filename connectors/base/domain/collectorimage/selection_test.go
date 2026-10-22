package collectorimage

import (
	"reflect"
	"testing"
)

func TestFromReferenceRequiresAnExplicitVersion(t *testing.T) {
	for _, ref := range []string{"", "registry.example.com/collector", "localhost:5000/collector", " registry.example.com/collector:v1", "registry.example.com/collector@sha256:1234"} {
		if _, err := FromReference(ref); err == nil {
			t.Errorf("accepted %q", ref)
		}
	}
	for _, ref := range []string{"registry.example.com/collector:v1", "localhost:5000/collector:v2", "registry.example.com/collector@" + digest} {
		pin, err := FromReference(ref)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := Resolve(pin, CloudAWS, ArchARM64, Override{})
		if err != nil || resolved.Ref != ref {
			t.Fatalf("roundtrip %q = %+v, %v", ref, resolved, err)
		}
	}
}

func TestConfigureRuntimeImageSelection(t *testing.T) {
	base, err := FromReference("registry.example.com/team/collector@" + digest)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		selection Selection
		want      string
	}{
		{"installation", nil, "registry.example.com/team/collector@" + digest},
		{"tag replaces digest", Selection{"tag": "v2"}, "registry.example.com/team/collector:v2"},
		{"registry with port", Selection{"registry": "localhost:5000"}, "localhost:5000/team/collector@" + digest},
		{"repository", Selection{"repository": "mirror.example.com/custom/collector"}, "mirror.example.com/custom/collector@" + digest},
		{"legacy repository", Selection{"repositoryOverride": "mirror.example.com/collector"}, "mirror.example.com/collector@" + digest},
		{"full reference", Selection{"reference": "mirror.example.com/new:v3"}, "mirror.example.com/new:v3"},
		{"digest wins", Selection{"tag": "v2", "digest": digest}, "registry.example.com/team/collector@" + digest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pin, err := Configure(base, CloudAWS, tc.selection)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Resolve(pin, CloudAWS, ArchAMD64, Override{})
			if err != nil || got.Ref != tc.want {
				t.Fatalf("got %+v, %v; want %s", got, err, tc.want)
			}
			if base.Repositories[CloudAWS] != "registry.example.com/team/collector" || base.Digest != digest {
				t.Fatal("mutated installation selection")
			}
		})
	}
}

func TestImageSelectionOverlay(t *testing.T) {
	base := Selection{"registry": "mirror.example.com", "digest": digest, "tag": "v1"}
	if got := Overlay(base, Selection{"tag": "v2"}); !reflect.DeepEqual(got, Selection{"registry": "mirror.example.com", "tag": "v2"}) {
		t.Fatalf("tag overlay = %#v", got)
	}
	if got := Overlay(base, Selection{"reference": "other.example.com/collector:v3"}); !reflect.DeepEqual(got, Selection{"reference": "other.example.com/collector:v3"}) {
		t.Fatalf("reference overlay = %#v", got)
	}
	if base["digest"] != digest {
		t.Fatal("overlay mutated defaults")
	}
}

func TestImageSelectionValidation(t *testing.T) {
	for _, selection := range []Selection{
		{"tag": 3}, {"typo": "v2"}, {"digest": "sha256:1234"}, {"registry": "https://mirror.example.com"},
		{"repository": "mirror.example.com/collector:v1"}, {"tag": "bad tag"}, {"reference": "mirror.example.com/collector"},
	} {
		if err := selection.Validate(); err == nil {
			t.Errorf("accepted %#v", selection)
		}
	}
	for _, selection := range []Selection{nil, {"tag": "v2"}, {"digest": ""}, {"registry": "localhost:5000"}, {"repositoryOverride": "mirror.example.com/collector"}} {
		if err := selection.Validate(); err != nil {
			t.Errorf("rejected %#v: %v", selection, err)
		}
	}
	if _, err := Resolve(Pin{}, CloudAWS, ArchAMD64, Override{}); err == nil {
		t.Fatal("missing installation and runtime image must refuse deployment")
	}
}
