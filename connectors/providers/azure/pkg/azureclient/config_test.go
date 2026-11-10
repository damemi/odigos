package azureclient

import "testing"

func TestResourceGroupFromID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{
			in:   "/subscriptions/sub/resourceGroups/rg-prod/providers/Microsoft.App/containerApps/app1",
			want: "rg-prod",
		},
		{
			in:   "/subscriptions/sub/resourcegroups/mixed-case/providers/Microsoft.Compute/virtualMachines/vm1",
			want: "mixed-case",
		},
		{in: "", want: ""},
		{in: "/subscriptions/sub/providers/Microsoft.App/containerApps/app1", want: ""},
	}
	for _, tc := range cases {
		if got := ResourceGroupFromID(tc.in); got != tc.want {
			t.Errorf("ResourceGroupFromID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
