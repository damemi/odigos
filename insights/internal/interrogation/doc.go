// Package interrogation runs on-demand interrogations of a workload: an ADK
// workflow that asks the model about the workload's call graph, adds the
// probes it proposes through the autopilot loop, and reports what it saw.
//
// A run is an ADK session. Sessions are stored in the insights Postgres
// database, in the adk schema, through ADK's gorm session service on a pool
// of their own.
package interrogation
