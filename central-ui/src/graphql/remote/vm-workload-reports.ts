import { Kind, OperationTypeNode, parse, print, type DocumentNode, type OperationDefinitionNode } from 'graphql';
import type { GetWorkloadReportsQuery } from '@odigos/vmagent-api-apollo-client';
import type { OperationContext } from '@odigos/ui-kit/contexts';
import { DesiredStateProgress, ProgrammingLanguages, type InstrumentationReport, type Workload } from '@odigos/ui-kit/types';
import { dialectVersion, vmQuery } from '@/graphql/remote/vm-dialect';
import { supportsWorkloadReportRegion } from '@/graphql/remote/utils';

export type VmWorkloadReport = GetWorkloadReportsQuery['workloadReports'][number] & { region?: string | null };

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

const mapReportStateToProgress = (state: string, enabled?: boolean | null): DesiredStateProgress => {
  switch (state as WorkloadReportState) {
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

const mapReportToInstrumentation = (report: VmWorkloadReport): InstrumentationReport => {
  const running = report.current?.runtime?.running;
  const language = running?.language ?? report.current?.runtimeName ?? null;
  const version = running?.version ?? report.current?.runtimeVersion ?? null;

  return {
    state: report.current?.state ?? WorkloadReportState.Unknown,
    instrumented: report.current?.instrumented ?? false,
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
  };
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

  switch (report.state as WorkloadReportState) {
    case WorkloadReportState.Instrumented:
      return {
        reasonEnum: HEALTH_REASON.SuccessAndEmittingTelemetry,
        message: detail || HEALTH_MESSAGE[WorkloadReportState.Instrumented],
      };
    case WorkloadReportState.Instrumenting:
      return {
        reasonEnum: stage || WorkloadReportState.Instrumenting,
        message: detail || HEALTH_MESSAGE[WorkloadReportState.Instrumenting],
      };
    case WorkloadReportState.Error:
      return {
        reasonEnum: stage || WorkloadReportState.Error,
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
        reasonEnum: stage || report.state || WorkloadReportState.Unknown,
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

  return {
    ...workload,
    instrumentationReport,
    workloadOdigosHealthStatus: {
      name: WORKLOAD_ODIGOS_HEALTH_NAME,
      status: progress,
      reasonEnum,
      message,
    },
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

  const fragments = reportsAst.definitions.filter((d) => d.kind === Kind.FRAGMENT_DEFINITION);
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
