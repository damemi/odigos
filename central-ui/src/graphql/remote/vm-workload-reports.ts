import { Kind, OperationTypeNode, parse, print, type DocumentNode, type FragmentDefinitionNode, type OperationDefinitionNode } from 'graphql';
import type { GetWorkloadReportsQuery } from '@odigos/vmagent-api-apollo-client';
import type { OperationContext } from '@odigos/ui-kit/contexts';
import { DesiredStateProgress, ProgrammingLanguages, type InstrumentationReport, type OtelDistroName, type Workload } from '@odigos/ui-kit/types';
import { dialectVersion, vmQuery } from '@/graphql/remote/vm-dialect';
import { rememberWorkloadRegion, supportsTypedContainerDetails, supportsWorkloadReportRegion, withFragmentSelections, type VmContainerOverride, type VmSourceConfig } from '@/graphql/remote/utils';

/** One `WorkloadReport.details.containers` row (agents-api v0.18.0+). */
interface VmWorkloadDetailsContainer {
  name: string;
  runtime?: {
    detected?: { language?: string | null; version?: string | null } | null;
    override?: { language?: string | null; version?: string | null } | null;
  } | null;
  instrumented?: boolean | null;
  distro?: string | null;
  /**
   * Whether Odigos can instrument this container, and why not when it cannot.
   *
   * This pair is the container's whole status: the reporter resolves every
   * platform-specific rule (ECS essentiality, whether Odigos injected the
   * container itself) and reports the outcome, so there is nothing for the UI to
   * re-derive and no per-platform flag for it to interpret.
   */
  instrumentationEligible?: boolean | null;
  excludedReason?: string | null;
}

/**
 * `WorkloadReport.details`, narrowed to the container layer central-ui consumes.
 *
 * The wire type is an interface with one branch per kind; the container-shaped
 * branches (ECS, docker, Cloud Run) all carry `containers`, and that is the only
 * part with a slot in the kit's `Workload` today. The process-shaped branches
 * leave `details` null, and Lambda's branch has no containers at all, so both
 * simply read as absent here.
 */
interface VmWorkloadDetails {
  containers?: VmWorkloadDetailsContainer[] | null;
}

export type VmWorkloadReport = GetWorkloadReportsQuery['workloadReports'][number] & { region?: string | null; details?: VmWorkloadDetails | null };

/** WorkloadReport.current.state values from agents-api / connectors. */
const WorkloadReportState = {
  Instrumented: 'Instrumented',
  Instrumenting: 'Instrumenting',
  NotInstrumented: 'NotInstrumented',
  Error: 'Error',
  Unsupported: 'Unsupported',
  NotImplemented: 'NotImplemented',
  Unknown: 'Unknown',
} as const;

type WorkloadReportState = (typeof WorkloadReportState)[keyof typeof WorkloadReportState];

const WORKLOAD_ODIGOS_HEALTH_NAME = 'WorkloadOdigosHealth';
const GET_SOURCES_WITH_WORKLOAD_REPORTS = 'GetSourcesWithWorkloadReports';

/** Default HealthCard reason/message when WorkloadReport has no progress/issue detail. */
const HEALTH_REASON = {
  SuccessAndEmittingTelemetry: 'SuccessAndEmittingTelemetry',
} as const;

const HEALTH_MESSAGE = {
  [WorkloadReportState.Instrumented]: 'source is instrumented, healthy and telemetry has been observed',
  [WorkloadReportState.Instrumenting]: 'source instrumentation is in progress',
  [WorkloadReportState.Error]: 'source instrumentation failed',
  [WorkloadReportState.Unsupported]: 'source runtime is not supported for instrumentation',
  [WorkloadReportState.NotImplemented]: 'instrumentation is not implemented for this source kind',
  [WorkloadReportState.NotInstrumented]: 'source is not instrumented',
} as const;

const reportKey = (serviceName: string, kind: string, region?: string | null) => `${serviceName}\0${kind}\0${region ?? ''}`;

/**
 * VM agent emits lowercase/snake_case stages (e.g. "instrumented", "waiting_for_rollout").
 * Connectors may send PascalCase ("Instrumented"). Normalize both to WorkloadReportState
 * so HealthCard matches the K8s Success / Waiting / Failure banners.
 */
const normalizeWorkloadReportState = (raw?: string | null): WorkloadReportState => {
  if (!raw?.trim()) return WorkloadReportState.Unknown;
  const key = raw
    .trim()
    .toLowerCase()
    .replace(/[\s-]+/g, '_');

  switch (key) {
    case 'instrumented':
      return WorkloadReportState.Instrumented;
    case 'instrumenting':
    case 'pending':
    case 'preparing':
    case 'upgrading':
    case 'waiting_for_rollout':
    case 'attaching':
      return WorkloadReportState.Instrumenting;
    case 'notinstrumented':
    case 'not_instrumented':
    case 'disabled':
      return WorkloadReportState.NotInstrumented;
    case 'error':
      return WorkloadReportState.Error;
    case 'unsupported':
      return WorkloadReportState.Unsupported;
    case 'notimplemented':
    case 'not_implemented':
      return WorkloadReportState.NotImplemented;
    case 'unknown':
      return WorkloadReportState.Unknown;
    default:
      return WorkloadReportState.Unknown;
  }
};

const mapReportStateToProgress = (state: string, enabled?: boolean | null): DesiredStateProgress => {
  switch (normalizeWorkloadReportState(state)) {
    case WorkloadReportState.Instrumented:
      return DesiredStateProgress.Success;
    case WorkloadReportState.Instrumenting:
      return DesiredStateProgress.Waiting;
    case WorkloadReportState.Error:
      return DesiredStateProgress.Failure;
    case WorkloadReportState.Unsupported:
    case WorkloadReportState.NotImplemented:
      return DesiredStateProgress.Unsupported;
    case WorkloadReportState.Unknown:
      return DesiredStateProgress.Unknown;
    case WorkloadReportState.NotInstrumented:
      return enabled ? DesiredStateProgress.Pending : DesiredStateProgress.Disabled;
    default:
      return DesiredStateProgress.Unknown;
  }
};

const mapLanguage = (language?: string | null): ProgrammingLanguages | null => {
  if (!language) return null;
  const normalized = language.trim().toLowerCase();
  const aliases: Record<string, ProgrammingLanguages> = {
    go: ProgrammingLanguages.Go,
    golang: ProgrammingLanguages.Go,
    javascript: ProgrammingLanguages.JavaScript,
    js: ProgrammingLanguages.JavaScript,
    node: ProgrammingLanguages.JavaScript,
    nodejs: ProgrammingLanguages.JavaScript,
    typescript: ProgrammingLanguages.JavaScript,
    python: ProgrammingLanguages.Python,
    java: ProgrammingLanguages.Java,
    dotnet: ProgrammingLanguages.DotNet,
    'dotnet-core': ProgrammingLanguages.DotNet,
    csharp: ProgrammingLanguages.CSharp,
    'c#': ProgrammingLanguages.CSharp,
    php: ProgrammingLanguages.Php,
    ruby: ProgrammingLanguages.Ruby,
    rust: ProgrammingLanguages.Rust,
    swift: ProgrammingLanguages.Swift,
    elixir: ProgrammingLanguages.Elixir,
  };
  return aliases[normalized] ?? ProgrammingLanguages.Unknown;
};

/** Turn snake_case agent stages into PascalCase reason titles (e.g. waiting_for_rollout → WaitingForRollout). */
const stageToReasonEnum = (stage: string, fallback: string): string => {
  const trimmed = stage.trim();
  if (!trimmed) return fallback;
  return trimmed
    .split(/[_\s-]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1).toLowerCase())
    .join('');
};

const mapReportToInstrumentation = (report: VmWorkloadReport): InstrumentationReport => {
  const running = report.current?.runtime?.running;
  const language = running?.language ?? report.current?.runtimeName ?? null;
  const version = running?.version ?? report.current?.runtimeVersion ?? null;
  const state = normalizeWorkloadReportState(report.current?.state ?? report.progress?.stage);

  return {
    state,
    instrumented: report.current?.instrumented ?? state === WorkloadReportState.Instrumented,
    sdkType: report.desired?.sdkType ?? null,
    progressStage: report.progress?.stage ?? null,
    progressMessage: report.progress?.message ?? null,
    waitingFor: report.progress?.waitingFor ?? null,
    runtimeLanguage: language,
    runtimeVersion: version,
    updatedAt: report.updatedAt ?? null,
    issues:
      report.issues?.map((issue) => ({
        code: issue.code,
        severity: issue.severity,
        message: issue.message,
        since: issue.since ?? null,
        transient: issue.transient ?? null,
      })) ?? null,
    processes: mapReportProcesses(report),
  };
};

/** Join WorkloadReport.instances + processMetadata by pid for the VM process list. */
const mapReportProcesses = (report: VmWorkloadReport): InstrumentationReport['processes'] => {
  const instances = report.instances ?? [];
  const metadata = report.processMetadata ?? [];
  if (!instances.length && !metadata.length) return null;

  const metaByPid = new Map(metadata.map((m) => [m.pid, m]));
  const seen = new Set<number>();
  const processes: NonNullable<InstrumentationReport['processes']> = [];

  for (const inst of instances) {
    seen.add(inst.pid);
    const meta = metaByPid.get(inst.pid);
    processes.push({
      pid: inst.pid,
      alive: inst.alive,
      exe: meta?.exe ?? null,
      cmdline: meta?.cmdline ?? null,
      instrumented: inst.instrumented,
      probesAttached: inst.probesAttached,
      message: inst.message ?? null,
      distro: inst.distro ?? null,
      instrumentationEligible: meta?.instrumentationEligible ?? null,
      excludedReason: meta?.excludedReason ?? null,
    });
  }

  // Metadata-only PIDs (e.g. excluded before an instance row exists).
  for (const meta of metadata) {
    if (seen.has(meta.pid)) continue;
    processes.push({
      pid: meta.pid,
      alive: true,
      exe: meta.exe ?? null,
      cmdline: meta.cmdline ?? null,
      instrumented: meta.instrumented,
      probesAttached: meta.probesAttached ?? null,
      message: null,
      distro: null,
      instrumentationEligible: meta.instrumentationEligible,
      excludedReason: meta.excludedReason ?? null,
    });
  }

  return processes.length ? processes : null;
};

/**
 * `WorkloadReport.details.containers` -> the kit's `Workload.containers`.
 *
 * A container-shaped workload (an ECS task definition packing several apps) has a
 * layer the flat report cannot express, and `Workload.containers` is the slot the
 * kit already models it in — same shape as k8s, so a container row renders the
 * same way whatever platform it came from. K8s is untouched: this only runs on
 * the agents-api mapping path, off `details`, which no k8s proxy serves.
 *
 * `runtimeInfo` is the DETECTED runtime and `overrides.runtimeInfo` the override,
 * because the kit derives the effective runtime as `overrides ?? detected`
 * (`getEffectiveLanguage`) — writing the effective value into both would make
 * every container look overridden. The distro override comes from the source's
 * `containerOverrides` (customer intent) while `agentEnabled.otelDistroName` is
 * the distro actually applied (reported status).
 *
 * `instrumentationEligible` + `excludedReason` become the container's
 * `agentEnabledStatus`, which is the kit's per-container status slot (ContainersCard
 * already renders its `message` as the badge tooltip). The report has no
 * platform-specific container flags to interpret — an ECS container the platform
 * does not treat as part of the application, or the agents sidecar Odigos injected
 * itself, both arrive as an ineligible container whose reason says which.
 */
const CONTAINER_AGENT_ENABLED_NAME = 'AgentEnabled';

/**
 * A container's eligibility + reason as the kit's per-container Condition.
 *
 * `reasonEnum` is derived from `instrumented` / `instrumentationEligible` only —
 * never from the reason text. The reason is prose written by the reporter for a
 * human ("Odigos agents sidecar", "eBPF is not available on Fargate"), and pattern
 * matching it here would put central-ui straight back to reading facts out of
 * strings, which is what dropping the platform-specific container flags avoided.
 */
const mapContainerStatus = (container: VmWorkloadDetailsContainer) => {
  const eligible = container.instrumentationEligible;
  const message = container.excludedReason?.trim() || '';

  if (container.instrumented) {
    return { name: CONTAINER_AGENT_ENABLED_NAME, status: DesiredStateProgress.Success, reasonEnum: 'Instrumented', message };
  }
  if (eligible === false) {
    // Not an error: the reporter decided this container is out of scope and said why.
    return { name: CONTAINER_AGENT_ENABLED_NAME, status: DesiredStateProgress.Unsupported, reasonEnum: 'NotEligible', message };
  }
  return { name: CONTAINER_AGENT_ENABLED_NAME, status: DesiredStateProgress.Pending, reasonEnum: 'NotInstrumented', message };
};

const mapReportContainers = (report: VmWorkloadReport, containerOverrides: VmContainerOverride[]): Workload['containers'] => {
  const containers = report.details?.containers ?? [];
  if (!containers.length) return null;

  const overrideByName = new Map(containerOverrides.map((o) => [o.containerName, o]));

  return containers.map((container) => {
    const override = overrideByName.get(container.name);
    const detected = container.runtime?.detected;
    const runtimeOverride = container.runtime?.override ?? override?.runtimeOverride;
    const distroOverride = override?.distroOverride?.otelDistroName || undefined;
    const hasRuntimeOverride = !!(runtimeOverride?.language || runtimeOverride?.version);

    return {
      containerName: container.name,
      runtimeInfo: detected?.language || detected?.version ? { language: mapLanguage(detected?.language) ?? ProgrammingLanguages.Unknown, runtimeVersion: detected?.version ?? null } : null,
      agentEnabled: {
        agentEnabled: !!container.instrumented,
        otelDistroName: (container.distro as OtelDistroName | undefined) ?? null,
        agentEnabledStatus: mapContainerStatus(container),
      },
      overrides:
        hasRuntimeOverride || distroOverride
          ? {
              containerName: container.name,
              otelDistroName: (distroOverride as OtelDistroName | undefined) ?? null,
              runtimeInfo: hasRuntimeOverride ? { language: mapLanguage(runtimeOverride?.language) ?? ProgrammingLanguages.Unknown, runtimeVersion: runtimeOverride?.version ?? null } : null,
            }
          : null,
    };
  });
};

/**
 * Map WorkloadReport state into the same Condition shape HealthCard expects
 * (reasonEnum + message), so connector/VM Overview matches the K8s banner.
 */
const mapReportToHealthPresentation = (report: InstrumentationReport): { reasonEnum: string; message: string } => {
  const progressMessage = report.progressMessage?.trim() || '';
  const firstIssue = report.issues?.find((i) => i.message)?.message?.trim() || '';
  const detail = progressMessage || firstIssue;
  const stage = report.progressStage?.trim() || '';
  const state = normalizeWorkloadReportState(report.state);

  switch (state) {
    case WorkloadReportState.Instrumented:
      return {
        reasonEnum: HEALTH_REASON.SuccessAndEmittingTelemetry,
        message: detail || HEALTH_MESSAGE[WorkloadReportState.Instrumented],
      };
    case WorkloadReportState.Instrumenting:
      return {
        reasonEnum: stageToReasonEnum(stage, WorkloadReportState.Instrumenting),
        message: detail || HEALTH_MESSAGE[WorkloadReportState.Instrumenting],
      };
    case WorkloadReportState.Error:
      return {
        reasonEnum: stageToReasonEnum(stage, WorkloadReportState.Error),
        message: detail || HEALTH_MESSAGE[WorkloadReportState.Error],
      };
    case WorkloadReportState.Unsupported:
      return {
        reasonEnum: WorkloadReportState.Unsupported,
        message: detail || HEALTH_MESSAGE[WorkloadReportState.Unsupported],
      };
    case WorkloadReportState.NotImplemented:
      return {
        reasonEnum: WorkloadReportState.NotImplemented,
        message: detail || HEALTH_MESSAGE[WorkloadReportState.NotImplemented],
      };
    case WorkloadReportState.NotInstrumented:
      return {
        reasonEnum: WorkloadReportState.NotInstrumented,
        message: detail || HEALTH_MESSAGE[WorkloadReportState.NotInstrumented],
      };
    default:
      return {
        reasonEnum: stageToReasonEnum(stage, report.state || WorkloadReportState.Unknown),
        message: detail,
      };
  }
};

const applyReportToWorkload = (workload: Workload, report: VmWorkloadReport): Workload => {
  const instrumentationReport = mapReportToInstrumentation(report);
  const enabled = report.desired?.sourceEnabled ?? report.desired?.enabled ?? workload.markedForInstrumentation?.markedForInstrumentation;
  const progress = mapReportStateToProgress(instrumentationReport.state, enabled);
  const { reasonEnum, message } = mapReportToHealthPresentation(instrumentationReport);
  const language = mapLanguage(instrumentationReport.runtimeLanguage);
  // The Source carries the customer's per-container intent, the report carries what
  // was applied; the container rows need both, and the Source mapping already ran.
  const containers = mapReportContainers(report, (workload.sourceConfig as VmSourceConfig | null | undefined)?.containerOverrides ?? []);

  return {
    ...workload,
    instrumentationReport,
    workloadOdigosHealthStatus: {
      name: WORKLOAD_ODIGOS_HEALTH_NAME,
      status: progress,
      reasonEnum,
      message,
    },
    ...(containers ? { containers } : {}),
    ...(language
      ? {
          runtimeInfo: {
            detectedLanguages: [language],
          },
        }
      : {}),
  };
};

/** Merge agents-api WorkloadReports onto Source-mapped Workloads by name+kind+region. */
export const mergeVmWorkloadReports = (workloads: Workload[], reports: VmWorkloadReport[]): Workload[] => {
  if (!reports.length) return workloads;

  const byKey = new Map<string, VmWorkloadReport>();
  for (const report of reports) {
    // The kit's bulk-persist payload drops region, so remember it here too — a
    // source disabled from the sources list needs it just as much as one created
    // from the discovery drawer. See the region side-channel in vm-sources.ts.
    rememberWorkloadRegion(report.kind, report.serviceName, report.region);
    byKey.set(reportKey(report.serviceName, report.kind, report.region), report);
  }

  return workloads.map((workload) => {
    const report = byKey.get(reportKey(workload.id.name, workload.id.kind, workload.id.region));
    return report ? applyReportToWorkload(workload, report) : workload;
  });
};

// Combine printed GetSources + GetWorkloadReports into one REMOTE_FETCH document
// so list/by-id can enrich Sources with instrumentation status in a single round-trip.
const combinePrintedQueries = (sourcesQuery: string, reportsQuery: string): string => {
  const sourcesAst = parse(sourcesQuery);
  const reportsAst = parse(reportsQuery);

  const sourcesOp = sourcesAst.definitions.find((d): d is OperationDefinitionNode => d.kind === Kind.OPERATION_DEFINITION);
  const reportsOp = reportsAst.definitions.find((d): d is OperationDefinitionNode => d.kind === Kind.OPERATION_DEFINITION);
  if (!sourcesOp || !reportsOp) return sourcesQuery;

  // Both sides can define fragments — agents-api v0.18.0 gave GetSources
  // `ContainerOverrideFields`, where earlier snapshots had none — so carrying
  // over only one side leaves the combined operation spreading a fragment it
  // never defines ("Unknown fragment ..."). Dedupe by name: both documents come
  // from the same dialect snapshot, so a shared name is the same fragment, and
  // printing it twice is itself a validation error.
  const fragmentsByName = new Map<string, FragmentDefinitionNode>();
  for (const definition of [...sourcesAst.definitions, ...reportsAst.definitions]) {
    if (definition.kind === Kind.FRAGMENT_DEFINITION) fragmentsByName.set(definition.name.value, definition);
  }
  const fragments = Array.from(fragmentsByName.values());

  const combined: DocumentNode = {
    kind: Kind.DOCUMENT,
    definitions: [
      {
        kind: Kind.OPERATION_DEFINITION,
        operation: OperationTypeNode.QUERY,
        name: { kind: Kind.NAME, value: GET_SOURCES_WITH_WORKLOAD_REPORTS },
        selectionSet: {
          kind: Kind.SELECTION_SET,
          selections: [...sourcesOp.selectionSet.selections, ...reportsOp.selectionSet.selections],
        },
      },
      ...fragments,
    ],
  };
  return print(combined);
};

/**
 * Build the agents-api query used by GET_WORKLOADS / GET_WORKLOADS_BY_IDS.
 * Prefer a combined sources+workloadReports document when the dialect supports
 * reports; otherwise fall back to sources-only (older VM schemas).
 */
export const buildVmSourcesWithReportsQuery = (ctx: OperationContext): string | undefined => {
  const sourcesQuery = vmQuery(ctx, (d) => d.GetSourcesDocument);
  if (!sourcesQuery) return undefined;

  const reportsQuery = vmQuery(ctx, (d) => d.GetWorkloadReportsDocument);
  if (!reportsQuery) return sourcesQuery;

  const combined = combinePrintedQueries(sourcesQuery, reportsQuery);
  // agents-api >= v0.16.0 adds `region` on WorkloadReport; patch older printed
  // dialect snapshots the same way GET_NAMESPACES_WITH_WORKLOADS does.
  return supportsWorkloadReportRegion(dialectVersion(ctx)) ? withWorkloadReportRegion(combined) : combined;
};

// The published vmagent dialect snapshots through v0.15.0 omit `region` on
// WorkloadReportFields. agents-api v0.16.0 (and connectors) require it for
// regional source identity. Inject the field into a printed GetWorkloadReports
// document when the connected platform's schema supports it.
export const withWorkloadReportRegion = (printedQuery: string): string => {
  if (/\bregion\b/.test(printedQuery)) return printedQuery;
  return printedQuery.replace(/(fragment WorkloadReportFields on WorkloadReport \{\s*serviceName\s*\n\s*kind)/, '$1\n  region');
};

// `WorkloadReport.details` (agents-api v0.18.0+), narrowed to what
// `mapReportContainers` consumes.
//
// Only the container-shaped branches are selected: Lambda's branch has no
// containers, and the ECS/Cloud Run workload scalars (clusterArn, taskDefinition,
// launchType, revision) have nowhere to go in the kit's `Workload` yet — asking
// for them would just grow every report payload. `runtime.running` is skipped for
// the same reason: the kit computes the effective runtime from detected+override
// itself. Add a field here when, and only when, something renders it.
//
// `instrumentationEligible` + `excludedReason` are the container's status, and the
// report carries no platform-specific container flags at all — an ECS container the
// platform does not treat as part of the application and the agents sidecar Odigos
// injected itself both arrive here as an ineligible container whose reason says
// which, so this pair is the whole story and there is nothing else to ask for.
const WORKLOAD_DETAILS_CONTAINER_FIELDS = `
  name
  runtime {
    detected { language version }
    override { language version }
  }
  instrumented
  instrumentationEligible
  excludedReason
  distro
`;

const WORKLOAD_DETAILS_SELECTIONS = `
  details {
    ... on EcsWorkloadDetails { containers { ${WORKLOAD_DETAILS_CONTAINER_FIELDS} } }
    ... on DockerWorkloadDetails { containers { ${WORKLOAD_DETAILS_CONTAINER_FIELDS} } }
    ... on CloudRunWorkloadDetails { containers { ${WORKLOAD_DETAILS_CONTAINER_FIELDS} } }
  }
`;

/**
 * Patch `details` into a printed document's WorkloadReport fragment when the
 * connected platform's schema has it. No-op below v0.18.0 — and no-op for k8s,
 * which never resolves an agents-api document at all.
 */
export const withWorkloadReportDetails = (printedQuery: string, ctx: OperationContext): string => {
  if (!supportsTypedContainerDetails(dialectVersion(ctx))) return printedQuery;
  return withFragmentSelections(printedQuery, 'WorkloadReport', WORKLOAD_DETAILS_SELECTIONS);
};
