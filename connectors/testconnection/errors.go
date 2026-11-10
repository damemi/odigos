package testconnection

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// Every probe error fails verification. A connectivity failure or unavailable
// API is not proof of permissions, and needs different guidance from a denial.
func discoveryCheck(check CheckResult, err error, summarize func(error) string, isAuth func(error) bool) CheckResult {
	check.Passed = err == nil
	if err == nil {
		check.Remediation = ""
		return check
	}
	check.Message = check.DisplayName + ": " + summarize(err)
	if !isAuth(err) {
		check.Remediation = "check connectivity from Central to the provider API, confirm the service is available/enabled, and retry"
	}
	return check
}

// SafeMessage retains provider diagnostics while removing supplied secret
// values. SDK initialization and authentication errors can echo their input.
func SafeMessage(provider string, credentials map[string]string, message string) string {
	s, ok := spec.Get(provider)
	if !ok {
		return message
	}
	var secrets []string
	for _, method := range s.Auth.Methods {
		for _, field := range method.Fields {
			value := credentials[field.Name]
			if (!field.Secret && field.Name != "accessKeyId") || value == "" {
				continue
			}
			secrets = append(secrets, value)
			// A service account JSON can be quoted by the SDK or only its private
			// key may appear in an error. Redact both encodings as well.
			var document map[string]any
			if json.Unmarshal([]byte(value), &document) == nil {
				for _, key := range []string{"private_key", "private_key_id", "client_secret", "refresh_token"} {
					if secret, ok := document[key].(string); ok && secret != "" {
						secrets = append(secrets, secret)
					}
				}
			}
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		quoted, _ := json.Marshal(secret)
		message = strings.ReplaceAll(message, string(quoted[1:len(quoted)-1]), "[REDACTED]")
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	return message
}
