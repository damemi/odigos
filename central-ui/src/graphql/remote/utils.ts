// Shared helpers for the VM (agents-api) remote mappers.

// Trim a string and collapse empty/whitespace-only values to `undefined` so
// optional fields are omitted from the request rather than sent as `""` (the
// agent treats empty strings as present, and empty configs fail validation).
export const stripEmpty = (value: string | null | undefined): string | undefined => {
  const trimmed = value?.trim();
  return trimmed ? trimmed : undefined;
};

// WorkloadReport.region landed in agents-api v0.16.0. Older schemas reject the
// field, so only inject it when the connected platform reports >= that version.
export const supportsWorkloadReportRegion = (schemaVersion: string): boolean => {
  const m = schemaVersion.replace(/^v/, '').match(/^(\d+)\.(\d+)/);
  if (!m) return false;
  const major = Number(m[1]);
  const minor = Number(m[2]);
  return major > 0 || (major === 0 && minor >= 16);
};
