package migrate

// NewMigrator lets tests move the schema to a given version with the same
// migrations Apply runs.
var NewMigrator = newMigrator
