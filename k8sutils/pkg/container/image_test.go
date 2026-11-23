package container

import "testing"

func TestImageTag(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{"registry.odigos.io/odigos-demo-frontend:v0.1.44", "v0.1.44"},
		{"wsearle/odimall:api-gateway", "api-gateway"},
		{"nginx", "latest"},
		{"localhost:5000/team/app", "latest"},
		{"localhost:5000/team/app:1.2.3", "1.2.3"},
		{"app@sha256:959d1739054721c8d389a47ef6a07bb27f5d9d8871f8c3a9c516b1ebf28d1d99", ""},
		{"app:2.0@sha256:959d1739054721c8d389a47ef6a07bb27f5d9d8871f8c3a9c516b1ebf28d1d99", "2.0"},
		{"", ""},
		{"Invalid:Image:Ref", ""},
	}
	for _, tt := range tests {
		if got := ImageTag(tt.image); got != tt.want {
			t.Errorf("ImageTag(%q) = %q, want %q", tt.image, got, tt.want)
		}
	}
}
