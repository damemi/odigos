// Shared helpers for the VM (agents-api) remote mappers.

import { Kind, parse, print, visit, type OperationDefinitionNode, type SelectionNode, type SelectionSetNode } from 'graphql';
import type { SourceConfig } from '@odigos/ui-kit/types';

// Trim a string and collapse empty/whitespace-only values to `undefined` so
// optional fields are omitted from the request rather than sent as `""` (the
// agent treats empty strings as present, and empty configs fail validation).
export const stripEmpty = (value: string | null | undefined): string | undefined => {
  const trimmed = value?.trim();
  return trimmed ? trimmed : undefined;
};

// True when the reported agents-api schema version is >= 0.<minor>. The
// `major > 0` arm keeps every gate below satisfied once agents-api leaves 0.x.
const atLeastMinor = (schemaVersion: string, minor: number): boolean => {
  const m = schemaVersion.replace(/^v/, '').match(/^(\d+)\.(\d+)/);
  if (!m) return false;
  return Number(m[1]) > 0 || Number(m[2]) >= minor;
};

// Semver-shaped agents-api versions (`0.24.0`, `v0.19.0`). Local / replace
// builds report `(devel)`, `UNKNOWN-VERSION`, or the vm-agent binary string
// `development` — none of which parse here.
export const isParseableAgentsApiVersion = (schemaVersion: string | undefined): boolean =>
  !!schemaVersion && /^\d+\.\d+/.test(schemaVersion.replace(/^v/i, '').trim());

// agents-api v0.12.0 introduced the typed `actions` / `instrumentationRules`
// surface (and its create / update / delete documents). Older schemas only expose
// the untyped `computePlatform` reads.
export const supportsTypedActionsAndRules = (schemaVersion: string): boolean => atLeastMinor(schemaVersion, 12);

// WorkloadReport.region landed in agents-api v0.16.0. Older schemas reject the
// field, so only inject it when the connected platform reports >= that version.
export const supportsWorkloadReportRegion = (schemaVersion: string): boolean => atLeastMinor(schemaVersion, 16);

// Destination.region was added in the same schema release. Older agents reject
// it on reads, so the destination adapter only selects it from v0.16.0 onward.
export const supportsDestinationRegion = (schemaVersion: string): boolean => atLeastMinor(schemaVersion, 16);

// Action.region is required by cloud connectors so the shared actions surface
// can target one managed Collector. It landed with the managed-collector action
// schema consumed by connector builds reporting v0.28.0.
export const supportsActionRegion = (schemaVersion: string): boolean => atLeastMinor(schemaVersion, 28);

// agents-api v0.18.0 typed the kind-specific blocks that used to have nowhere to
// live: `WorkloadReport.details` (an ECS task's container layer, replacing the
// deprecated untyped `metadata: JSON`) and `Source.config.ecs` / `.docker`
// (per-container runtime/distro overrides). Both are additive. The published
// dialect registry floors to v0.19.0 when the platform reports >= that version
// (so those selections arrive natively); for a platform stuck between a newer
// schema and an older frozen snapshot the adapter still patches them in — but
// only when the connected platform actually has them, since an older agent
// rejects the whole query on one unknown field.
export const supportsTypedContainerDetails = (schemaVersion: string): boolean => atLeastMinor(schemaVersion, 18);

// Provider-specific instrumentation catalogs and Java handler adapters (agents-api v0.28.0).
export const supportsInstrumentationDistributions = (schemaVersion: string): boolean => atLeastMinor(schemaVersion, 28);

// Destination.disabled (pause export without delete) landed in agents-api v0.24.0.
// Older schemas reject the field on reads and on create/update inputs.
// Unparseable versions are treated as current: omitting the field on a local
// OSX/`development` agent makes UpdateDestination succeed without changing
// Disabled (the input is optional), which matches a success toast and no-op UI.
// patchVmDestinationsQuery is a no-op once the floored dialect already selects it.
export const supportsDestinationDisabled = (schemaVersion: string): boolean =>
  !isParseableAgentsApiVersion(schemaVersion) || atLeastMinor(schemaVersion, 24);

// DestinationType.testConnectionSupported (which destination types can be tested)
// lands in the agents-api release after v0.36.0. Older schemas reject the field,
// so unparseable (local / `development`) versions are treated as not supporting it.
export const supportsDestinationTestConnection = (schemaVersion: string): boolean =>
  isParseableAgentsApiVersion(schemaVersion) && atLeastMinor(schemaVersion, 37);

// Region side-channel.
//
// Cloud connectors key source identity on name+kind+REGION and reject a create
// without a region. The kit's bulk-persist payload, however, is k8s-shaped and
// carries exactly `{ namespace, name, kind, selected, currentStreamName }` — it
// has no region field and builds each entry explicitly, so a region we attach to
// a workload id is dropped before `buildVmPersistSources` ever sees it. Reading
// `s.region` there always yields undefined.
//
// The workload reports we map DO carry region, and name+kind survive the kit
// intact, so remember each discovered workload's region while mapping and
// recover it at persist time. Keyed on kind+name because that is the identity
// the kit preserves.
const workloadRegions = new Map<string, string>();

const regionKey = (kind: string, name: string): string => `${kind}\0${name}`;

// Only CLOUD CONNECTOR workloads carry a region, and only they accept one: a
// connector keys source identity on name+kind+region and rejects a create
// without it, while every other platform is region-less and would be handed
// identity it does not use.
//
// Connector kinds are provider-prefixed — "aws.fargate-task", "aws.lambda",
// "gcp.cloud-run". The region-less kinds are all bare names with no prefix: the
// VM agent's (docker, systemd, process), the ECS agent's (ecs-docker), and
// Kubernetes' (Deployment, StatefulSet, DaemonSet, …). The prefix is therefore
// the discriminator, and gating on it means a stray region on a VM or k8s
// workload can never leak into a mutation.
export const isConnectorWorkloadKind = (kind: string | null | undefined): boolean => !!kind && /^[a-z0-9]+\.[a-z0-9-]+$/.test(kind);

/** Remember a connector workload's region so persist can recover it. No-op for region-less platforms. */
export const rememberWorkloadRegion = (kind: string | null | undefined, name: string | null | undefined, region: string | null | undefined): void => {
  if (!kind || !name || !region || !isConnectorWorkloadKind(kind)) return;
  workloadRegions.set(regionKey(kind, name), region);
};

/** The remembered region for a connector workload; undefined for VM / ECS-agent / k8s kinds. */
export const lookupWorkloadRegion = (kind: string | null | undefined, name: string | null | undefined): string | undefined => {
  if (!kind || !name || !isConnectorWorkloadKind(kind)) return undefined;
  return workloadRegions.get(regionKey(kind, name));
};

/** Exported for tests: drop everything remembered. */
export const clearWorkloadRegions = (): void => workloadRegions.clear();

// ---------------------------------------------------------------------------
// Patching selections into a frozen dialect snapshot.
//
// A VM/connector document comes from the published dialect registry, floored to
// the highest FROZEN snapshot that is <= the reported schema (v0.19.0 today).
// A platform reporting a schema newer than every frozen snapshot therefore
// serves fields the printed document never asks for. The adapter's `patchQuery`
// hook exists for exactly that, and these helpers do the patching on the parsed
// AST rather than by string surgery, so they survive any `print` formatting
// change and any reordering inside the snapshot. When the floored dialect
// already selects the field, merge is a no-op (marker match).
// ---------------------------------------------------------------------------

/** Parse a printed selection-set body (`"a b { c }"`) into selection nodes. */
const parseSelections = (body: string): readonly SelectionNode[] => {
  const op = parse(`{ ${body} }`).definitions.find((d): d is OperationDefinitionNode => d.kind === Kind.OPERATION_DEFINITION);
  return op?.selectionSet.selections ?? [];
};

/**
 * Append `extra` to `set`, or return undefined to leave it alone when the first
 * injected field is already selected — a future snapshot that ships the
 * selection natively must not be patched twice.
 */
const mergeSelections = (set: SelectionSetNode, extra: readonly SelectionNode[], marker: string): SelectionSetNode | undefined => {
  if (set.selections.some((s) => s.kind === Kind.FIELD && s.name.value === marker)) return undefined;
  return { ...set, selections: [...set.selections, ...extra] };
};

/** Name of the first field in `body`; used both as the dedupe marker and as a sanity check. */
const markerField = (extra: readonly SelectionNode[]): string | undefined => {
  const first = extra.find((s) => s.kind === Kind.FIELD);
  return first?.kind === Kind.FIELD ? first.name.value : undefined;
};

/**
 * Append `body` to the selection set of every field named `field`. No-op when
 * the document has no such field (or it already carries `body`).
 */
export const withFieldSelections = (printedQuery: string, field: string, body: string): string => {
  const extra = parseSelections(body);
  const marker = markerField(extra);
  if (!marker) return printedQuery;

  let patched = false;
  const next = visit(parse(printedQuery), {
    Field(node) {
      if (node.name.value !== field || !node.selectionSet) return undefined;
      const selectionSet = mergeSelections(node.selectionSet, extra, marker);
      if (!selectionSet) return undefined;
      patched = true;
      return { ...node, selectionSet };
    },
  });
  return patched ? print(next) : printedQuery;
};

/**
 * Append `body` to every fragment defined on `typeCondition`. Reports are
 * selected through a fragment (`...WorkloadReportFields`), so the fragment
 * definition — not a field — is the injection point there.
 */
export const withFragmentSelections = (printedQuery: string, typeCondition: string, body: string): string => {
  const extra = parseSelections(body);
  const marker = markerField(extra);
  if (!marker) return printedQuery;

  let patched = false;
  const next = visit(parse(printedQuery), {
    FragmentDefinition(node) {
      if (node.typeCondition.name.value !== typeCondition) return undefined;
      const selectionSet = mergeSelections(node.selectionSet, extra, marker);
      if (!selectionSet) return undefined;
      patched = true;
      return { ...node, selectionSet };
    },
  });
  return patched ? print(next) : printedQuery;
};

// ---------------------------------------------------------------------------
// Per-container source config (agents-api v0.18.0+).
// ---------------------------------------------------------------------------

/**
 * One entry of `Source.config.{ecs,docker}.containerOverrides`.
 *
 * Mirrors the wire / ui-kit `SourceContainerOverride` shape. Kept as a local
 * interface so central-ui typechecks against ui-kit releases that predate the
 * `SourceConfig.containerOverrides` slot; once that ships, this is identical.
 */
export interface VmContainerOverride {
  containerName: string;
  runtimeOverride?: { language?: string | null; version?: string | null } | null;
  distroOverride?: { otelDistroName?: string | null } | null;
  /** Opt-OUT of instrumenting this container; absent/false means "no opinion". */
  disabled?: boolean | null;
}

/**
 * Workload.sourceConfig with the container layer. Intersection stays until every
 * deployed ui-kit includes `SourceConfig.containerOverrides` natively.
 */
export type VmSourceConfig = SourceConfig & { containerOverrides?: VmContainerOverride[] | null };

/** A source's `config` as central-ui last read it, workload level plus container layer. */
export interface VmSourceConfigSnapshot {
  runtimeOverride?: { language?: string | null; version?: string | null } | null;
  distroOverride?: { otelDistroName?: string | null } | null;
  containerOverrides: VmContainerOverride[];
}

/**
 * Which kind-specific `SourceConfig` branch a workload kind accepts, or undefined
 * for the kinds that accept neither (Lambda, Cloud Run, systemd, process, k8s).
 *
 * Mirrors the VM agent's `ConfigBranchForKind` (vm-agent
 * `pkg/components/domain/source/source.go`): the branch names a platform family,
 * so every ECS kind takes `ecs` while the VM agent's flat `docker` and `podman`
 * kinds take `docker` (the agent stores Podman settings in the Docker branch).
 * The server rejects a branch that disagrees with the kind,
 * so this is the gate on the write path — never guess a branch.
 */
export const configBranchForKind = (kind: string | null | undefined): 'ecs' | 'docker' | undefined => {
  if (!kind) return undefined;
  if (kind.startsWith('aws.fargate-task') || kind.startsWith('aws.ecs')) return 'ecs';
  if (kind === 'docker' || kind === 'podman') return 'docker';
  return undefined;
};

// Source-config side-channel.
//
// `updateSource` REPLACES `Source.config` wholesale (see ApplyUpdateInput in the
// connector), and the kit's update payload is the flat `SourceFormData` — one
// container name plus one runtime/distro pair, with no room for the other
// containers. Sending a config built from that payload alone would silently
// delete every per-container override the customer configured elsewhere (odictl,
// another container's row), which is a data loss the server cannot detect.
//
// So remember each source's config while mapping it and replay the untouched
// parts on update. Same mechanism as the region side-channel above, keyed the
// same way plus region: the same name+kind can exist in two regions with
// different overrides.
const sourceConfigs = new Map<string, VmSourceConfigSnapshot>();

const sourceConfigKey = (kind: string, name: string, region?: string | null): string => `${kind}\0${name}\0${region ?? ''}`;

/**
 * Remember a source's config exactly as read. Always call this when the config
 * selection was actually requested, including when nothing is configured —
 * recording the empty state is what lets a cleared override stay cleared.
 */
export const rememberSourceConfig = (kind: string | null | undefined, name: string | null | undefined, region: string | null | undefined, config: VmSourceConfigSnapshot): void => {
  if (!kind || !name) return;
  sourceConfigs.set(sourceConfigKey(kind, name, region), config);
};

/** The config last read for a source; undefined when it was never mapped. */
export const lookupSourceConfig = (kind: string | null | undefined, name: string | null | undefined, region: string | null | undefined): VmSourceConfigSnapshot | undefined => {
  if (!kind || !name) return undefined;
  return sourceConfigs.get(sourceConfigKey(kind, name, region));
};

/** Exported for tests: drop everything remembered. */
export const clearSourceConfigs = (): void => sourceConfigs.clear();
