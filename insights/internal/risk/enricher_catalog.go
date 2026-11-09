package risk

import "github.com/odigos-io/odigos/insights/internal/repo"

// Catalog of context-enricher signals - the counterpart to the deviation-class
// Catalog in risk.go. A class fires a single base signal; an enricher adds
// extra signals for dangerous sub-cases of that class's deviation (e.g. a new
// DB access that is specifically a DELETE), so the score reflects how bad the
// deviation is, not just that it happened.
//
// To add an enricher: (1) add a signal below with Parent set to the class it
// refines, and (2) emit it - implement the RiskEnricher capability on that
// class's scorer for single-class evidence (see engine's EnrichRisk), or a
// crossClassEnricher in engine/enrich.go for cross-class correlation. Optionally
// give it a List for a per-scope editable keyword list.

// EnricherList is an optional per-scope editable keyword list attached to an
// enricher signal (e.g. the sensitive-table keywords for d6_sensitive_table).
// It is the single definition the enricher reads its default from and the policy
// editor renders a textarea for; the list is stored generically in
// Policy.EnricherLists keyed by Key, so a config-driven enricher needs no new
// column or template code.
type EnricherList struct {
	Key     repo.EnricherListKey // stable id used in storage + the edit form, e.g. "sensitive_tables"
	Label   string               // UI label, e.g. "Sensitive keywords"
	Hint    string               // one-line UI hint shown under the textarea
	Default []string             // built-in default list, pre-filled in the editor
}

// EnricherSignal describes a signal produced by a context enricher rather than a
// deviation class (e.g. "the new DB access was a DELETE"). It is the single
// definition shared by the enricher that emits it and the policy editor that
// tunes it: Source is the stable override id, Parent the class it nests under,
// Label/Desc the display text, Weight the default contribution, and List an
// optional per-scope keyword list.
type EnricherSignal struct {
	Source   repo.SignalSource
	Parent   repo.DeviationClass
	Category Category
	Weight   int
	Label    string
	Desc     string
	List     *EnricherList
}

// EnricherSignals is the catalog of tunable enricher signals (see the file
// header for how to add one). Each nests under its Parent class and adds to
// that class's base weight when it fires; a scoped policy overrides Weight by
// Source (0 = off).
var EnricherSignals = []EnricherSignal{
	{
		Source: "d6_destructive_op", Parent: repo.D6DBAccess, Category: CategoryImpact,
		Weight: Categories[CategoryImpact].Base,
		Label:  "Destructive operation", Desc: "New DELETE/DROP/TRUNCATE/ALTER/UPDATE not seen during learning.",
	},
	{
		Source: "d6_sensitive_table", Parent: repo.D6DBAccess, Category: CategoryExfiltration, Weight: 30,
		Label: "Sensitive table access", Desc: "New access to a table whose name looks sensitive (credentials, payment, token…).",
		List: &EnricherList{
			Key:   "sensitive_tables",
			Label: "Sensitive keywords",
			Hint:  "Comma- or newline-separated substrings. A newly-accessed table whose name contains any of these is flagged. Pre-filled with the built-in defaults - edit to fit your schema.",
			Default: []string{
				"credential", "password", "passwd", "secret", "token", "apikey", "api_key",
				"payment", "card", "ssn", "account", "auth", "session", "admins", "users",
			},
		},
	},
	{
		Source: "d6_injection_shape", Parent: repo.D6DBAccess, Category: CategoryInjection, Weight: 35,
		Label: "Multi-table / join shape", Desc: "A new query joining several tables - a classic injection / data-harvesting shape.",
	},
	{
		Source: "d6_schema_recon", Parent: repo.D6DBAccess, Category: CategoryInjection, Weight: 30,
		Label: "Database schema recon", Desc: "New access to a DB system-catalog / metadata table (e.g. information_schema) - classic SQL-injection enumeration.",
		List: &EnricherList{
			Key:   "schema_catalog_tables",
			Label: "Schema-catalog tables",
			Hint:  "Comma- or newline-separated names/substrings. A newly-accessed table matching any is flagged as schema reconnaissance. Pre-filled with common DB system-catalog objects - edit to fit your databases.",
			Default: []string{
				"information_schema", "pg_catalog", "pg_class", "pg_tables", "pg_attribute",
				"pg_shadow", "pg_user", "mysql.user", "mysql.db", "sqlite_master",
				"sys.tables", "sys.columns", "sys.databases", "all_tables", "dba_tables", "user_tables",
			},
		},
	},
	{
		Source: "d1_edge_fanout", Parent: repo.D1CallEdges, Category: CategoryLateralMovement, Weight: 30,
		Label: "Call fan-out", Desc: "One request reached many new services at once - possible lateral movement / enumeration.",
	},
	{
		Source: "d2_metadata_endpoint", Parent: repo.D2Egress, Category: CategoryExfiltration, Weight: 50,
		Label: "Cloud metadata endpoint", Desc: "New egress to a cloud metadata address (e.g. 169.254.169.254) - a hallmark of SSRF / credential theft.",
		List: &EnricherList{
			Key:   "metadata_endpoints",
			Label: "Cloud metadata addresses",
			Hint:  "Comma- or newline-separated hosts/IPs. A new egress to any of these is flagged. Pre-filled with the well-known cloud metadata addresses - add your provider's if missing.",
			Default: []string{
				"169.254.169.254", "fd00:ec2::254", "metadata.google.internal", "100.100.100.200",
			},
		},
	},
	{
		Source: "d2_dangerous_port", Parent: repo.D2Egress, Category: CategoryExfiltration, Weight: 45,
		Label: "Suspicious egress port", Desc: "New egress on a port commonly used by reverse shells, C2, or tunneling (e.g. 4444, 1337).",
		List: &EnricherList{
			Key:   "dangerous_ports",
			Label: "Suspicious ports",
			Hint:  "Comma- or newline-separated port numbers. New egress to any of these is flagged. Pre-filled with common reverse-shell / C2 / tunneling ports - add or remove to fit your environment.",
			Default: []string{
				"23", "1080", "1337", "2323", "4444", "6667", "6697", "9050", "12345", "31337",
			},
		},
	},
	{
		Source: "d7_response_exfil", Parent: repo.D7PayloadSize, Category: CategoryExfiltration, Weight: 40,
		Label: "Oversized response (bulk data)", Desc: "Response body far larger than the learned baseline - possible bulk data extraction.",
	},
	{
		Source: "d4_injection_signature", Parent: repo.D4ArgsReturns, Category: CategoryInjection, Weight: 40,
		Label: "Injection-like argument / return", Desc: "A deviating captured argument or return value matches a known injection pattern (SQLi, path traversal, command/template injection, SSRF).",
		List: &EnricherList{
			Key:   "injection_signatures",
			Label: "Injection signatures",
			Hint:  "Comma- or newline-separated substrings (case-insensitive). A deviating captured argument or return value containing any is flagged as an injection attempt. Pre-filled with common SQLi / traversal / command / template / SSRF markers - edit to fit your app.",
			Default: []string{
				"' or ", "\" or ", " or 1=1", "union select", "'--", "\"--", "/*", "*/", "; drop ", "'; ", "sleep(", "waitfor delay", "information_schema", "pg_sleep(",
				"../", "..\\", "/etc/passwd", "c:\\windows",
				"$(", "`", "&&", "|| ", "; rm ", "; cat ", "| nc ", "; nc ",
				"${", "#{", "{{", "<%",
				"169.254.169.254", "metadata.google.internal", "file://", "gopher://", "dict://",
				"<script", "javascript:", "onerror=",
			},
		},
	},
}

// EnricherWeight returns the default weight for an enricher signal source, and
// ok=false if the source is not a known enricher signal.
func EnricherWeight(source repo.SignalSource) (int, bool) {
	for _, e := range EnricherSignals {
		if e.Source == source {
			return e.Weight, true
		}
	}
	return 0, false
}

// EnrichersForClass returns the enricher signals that nest under a given
// deviation class (its dangerous sub-cases), in catalog order. Empty for a class
// with no enrichers.
func EnrichersForClass(class repo.DeviationClass) []EnricherSignal {
	var out []EnricherSignal
	for _, e := range EnricherSignals {
		if e.Parent == class {
			out = append(out, e)
		}
	}
	return out
}

// DefaultList returns the built-in default list for an enricher list key, or nil
// if no enricher defines that key. Used by both the engine (as the fallback when
// a scope has no override) and the editor (to pre-fill the textarea).
func DefaultList(key repo.EnricherListKey) []string {
	for _, e := range EnricherSignals {
		if e.List != nil && e.List.Key == key {
			return e.List.Default
		}
	}
	return nil
}
