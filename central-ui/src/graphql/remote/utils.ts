// Shared helpers for the VM (agents-api) remote mappers.

// Trim a string and collapse empty/whitespace-only values to `undefined` so
// optional fields are omitted from the request rather than sent as `""` (the
// agent treats empty strings as present, and empty configs fail validation).
export const stripEmpty = (value: string | null | undefined): string | undefined => {
  const trimmed = value?.trim();
  return trimmed ? trimmed : undefined;
};
