package deployer

import "testing"

func TestExecutionRoleARNIsRequiredAndAccountScoped(t *testing.T) {
	valid := "arn:aws:iam::" + testAccount + ":role/OdigosCollectorExecutionRole"
	for name, ref := range map[string]string{
		"missing":       "",
		"not an arn":    "OdigosCollectorExecutionRole",
		"wrong account": "arn:aws:iam::000000000000:role/OdigosCollectorExecutionRole",
		"not a role":    "arn:aws:iam::" + testAccount + ":policy/OdigosCollectorExecutionRole",
	} {
		t.Run(name, func(t *testing.T) {
			got, problems := ValidateExecutionRoleARN(ref, "aws", testAccount, testRegion)
			if got != "" || len(problems) != 1 || problems[0].Code != ProblemExecutionRoleRequired {
				t.Fatalf("validation = %q / %+v", got, problems)
			}
		})
	}

	got, problems := ValidateExecutionRoleARN(valid, "aws", testAccount, testRegion)
	if got != valid || len(problems) != 0 {
		t.Fatalf("valid role = %q / %+v", got, problems)
	}
}
