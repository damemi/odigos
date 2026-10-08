package awsclient

import "testing"

func TestValidateCollectorRoleRequiresRoleInConnectorAccount(t *testing.T) {
	const account = "025950698545"
	for _, tc := range []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{name: "same account role", ref: "arn:aws:iam::025950698545:role/OdigosCollectorDeploymentRole"},
		{name: "role with path", ref: "arn:aws:iam::025950698545:role/odigos/Collector"},
		{name: "different account", ref: "arn:aws:iam::123456789012:role/OdigosCollectorDeploymentRole", wantErr: true},
		{name: "user instead of role", ref: "arn:aws:iam::025950698545:user/OdigosConnector", wantErr: true},
		{name: "empty", wantErr: true},
		{name: "role name only", ref: "OdigosCollectorDeploymentRole", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCollectorRole(tc.ref, account)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateCollectorRole(%q) error = %v, want error %t", tc.ref, err, tc.wantErr)
			}
		})
	}
}
