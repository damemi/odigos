import type { GetInstrumentationInstructionsData } from '@odigos/ui-kit/contexts';
import type { InstructionDocument, InstructionInput, WorkloadId } from '@odigos/ui-kit/types';
import { lookupWorkloadRegion, stripEmpty } from '@/graphql/remote/utils';

/**
 * `instrumentationInstructions` — how the customer instruments one workload themselves.
 *
 * This is the manual-mode half of a cloud connector. A workload type the connector discovers but
 * does not instrument is instrumented BY THE CUSTOMER, in their own Terraform / CloudFormation /
 * Bicep, and the connector then observes the result. It is the DEFAULT for a discovered type, not
 * an edge case, so "not instrumented" without the instructions is half a report.
 *
 * Everything on the wire is finished text. The connector renders the change from the same recipes
 * its injector and its conformance check use, and substitutes this workload's endpoint, service
 * name and region before responding — so there is nothing here to template, interpret or re-derive,
 * and this mapper is deliberately a shape adapter and nothing more. Anything it computed would be a
 * second opinion competing with the one that decides whether the customer's deployment is reported
 * as drifted.
 */

/** One `instrumentationInstructions.documents` row as it arrives from the connector. */
interface VmInstructionDocument {
  id?: string | null;
  variant?: string | null;
  variantLabel?: string | null;
  title?: string | null;
  description?: string | null;
  steps?: (string | null)[] | null;
  format?: string | null;
  content?: string | null;
  unresolved?: ({ placeholder?: string | null; description?: string | null } | null)[] | null;
}

interface VmInstrumentationInstructions {
  serviceName?: string | null;
  kind?: string | null;
  region?: string | null;
  mode?: string | null;
  documents?: (VmInstructionDocument | null)[] | null;
  notes?: (string | null)[] | null;
  updatedAt?: string | null;
}

interface VmInstrumentationInstructionsResult {
  instrumentationInstructions?: VmInstrumentationInstructions | null;
}

const FALLBACK_VARIANT = 'manual';
const FALLBACK_FORMAT = 'text';

const strings = (values?: (string | null)[] | null): string[] => (values ?? []).filter((v): v is string => typeof v === 'string' && v.length > 0);

const mapInputs = (unresolved: VmInstructionDocument['unresolved']): InstructionInput[] | null => {
  const inputs = (unresolved ?? [])
    .filter((input): input is { placeholder?: string | null; description?: string | null } => !!input)
    .map((input) => ({ placeholder: input.placeholder ?? '', description: input.description ?? '' }))
    .filter((input) => !!input.placeholder);
  // Null rather than [] so the drawer's "values you still need to fill in" section is absent
  // instead of empty-with-a-heading for a fully resolved document.
  return inputs.length ? inputs : null;
};

const mapDocument = (doc: VmInstructionDocument): InstructionDocument => ({
  id: doc.id ?? '',
  // Both default rather than being dropped: the variant keys the chooser and the format keys the
  // code block, and a document with neither is still the instructions — it just has no tool to
  // claim and no syntax to highlight.
  variant: stripEmpty(doc.variant) ?? FALLBACK_VARIANT,
  variantLabel: stripEmpty(doc.variantLabel) ?? stripEmpty(doc.variant) ?? FALLBACK_VARIANT,
  title: doc.title ?? '',
  description: doc.description ?? '',
  steps: strings(doc.steps),
  format: stripEmpty(doc.format) ?? FALLBACK_FORMAT,
  content: doc.content ?? '',
  unresolved: mapInputs(doc.unresolved),
});

export const mapVmInstrumentationInstructions = (data: unknown): GetInstrumentationInstructionsData => {
  const raw = (data as VmInstrumentationInstructionsResult | null | undefined)?.instrumentationInstructions;
  if (!raw) return { instrumentationInstructions: null };

  const documents = (raw.documents ?? []).filter((doc): doc is VmInstructionDocument => !!doc).map(mapDocument);

  // A response with no renderable document is the same as no response. The connector promises at
  // least one (it falls back to describing the change in prose when it has no per-tool snippet), so
  // reaching here means a connector too old to keep that promise — and an empty card would read as
  // "there is nothing to do", which is the opposite of the truth for a manual-mode workload.
  if (!documents.length) return { instrumentationInstructions: null };

  return {
    instrumentationInstructions: {
      serviceName: raw.serviceName ?? '',
      kind: raw.kind ?? '',
      region: raw.region ?? null,
      mode: raw.mode ?? '',
      documents,
      notes: strings(raw.notes),
      updatedAt: raw.updatedAt ?? '',
    },
  };
};

/**
 * `WorkloadId` -> the connector's composite key.
 *
 * The region falls back to the one remembered from the workload list. It is part of the identity a
 * connector resolves a workload by, and a dialect snapshot that predates `WorkloadReport.region`
 * leaves it off the id — asking without it would resolve the wrong workload on an account running
 * the same service in two regions, or none at all.
 */
export const vmInstrumentationInstructionsVariables = (vars: WorkloadId): Record<string, unknown> => ({
  serviceName: vars.name,
  kind: vars.kind,
  region: stripEmpty(vars.region) ?? lookupWorkloadRegion(vars.kind, vars.name) ?? null,
});
