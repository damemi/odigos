package consts

import (
	"testing"
)

func TestGetCentralUIHost(t *testing.T) {
	tests := []struct {
		name        string
		uiHost      string
		backendHost string
		want        string
	}{
		{
			name: "default when unset",
			want: "http://localhost:3000",
		},
		{
			name:   "explicit UI host",
			uiHost: "https://central.example.com",
			want:   "https://central.example.com",
		},
		{
			name:        "local port-forward misconfiguration uses UI port",
			uiHost:      "http://localhost:8081",
			backendHost: "http://localhost:8081",
			want:        "http://localhost:3000",
		},
		{
			name:        "production shared origin unchanged",
			uiHost:      "https://central.example.com",
			backendHost: "https://central.example.com",
			want:        "https://central.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CENTRAL_UI_HOST", tt.uiHost)
			t.Setenv("CENTRAL_BACKEND_HOST", tt.backendHost)

			if got := GetCentralUIHost(); got != tt.want {
				t.Fatalf("GetCentralUIHost() = %q, want %q", got, tt.want)
			}
		})
	}
}
