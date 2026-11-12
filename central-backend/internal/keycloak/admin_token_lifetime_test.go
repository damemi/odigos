package keycloak

import (
	"testing"
	"time"
)

func TestUsableAdminTokenLifetime(t *testing.T) {
	tests := []struct {
		name             string
		expiresIn        int
		refreshExpiresIn int
		want             time.Duration
	}{
		{name: "session idle shorter than jwt", expiresIn: 36000, refreshExpiresIn: 1800, want: 1800 * time.Second},
		{name: "jwt shorter than refresh", expiresIn: 60, refreshExpiresIn: 1800, want: 60 * time.Second},
		{name: "missing jwt expiry uses refresh", expiresIn: 0, refreshExpiresIn: 1800, want: 1800 * time.Second},
		{name: "missing refresh uses jwt", expiresIn: 60, refreshExpiresIn: 0, want: 60 * time.Second},
		{name: "neither lifetime", expiresIn: 0, refreshExpiresIn: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usableAdminTokenLifetime(tt.expiresIn, tt.refreshExpiresIn); got != tt.want {
				t.Fatalf("usableAdminTokenLifetime(%d, %d) = %s, want %s", tt.expiresIn, tt.refreshExpiresIn, got, tt.want)
			}
		})
	}
}
