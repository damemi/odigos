import type { ManagedCollectorInstructions, ManagedCollectorReport } from '@odigos/ui-kit/types';

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null;

/** Keep fields the dialect query returns, including ones the kit type treats as optional. */
export const mapManagedCollectorReport = (raw: unknown): ManagedCollectorReport | undefined => {
  if (!isRecord(raw) || typeof raw.region !== 'string' || !isRecord(raw.desired)) return undefined;
  const desired = raw.desired;
  const report = raw as unknown as ManagedCollectorReport;
  return {
    ...report,
    networkId: typeof raw.networkId === 'string' ? raw.networkId : report.networkId,
    issues: Array.isArray(raw.issues) ? report.issues : [],
    desired: {
      ...report.desired,
      cpu: typeof desired.cpu === 'string' ? desired.cpu : '',
      memory: typeof desired.memory === 'string' ? desired.memory : '',
      enabled: desired.enabled !== false,
    },
    authority: report.authority ?? { mode: '', origin: '', attribution: '', mutable: false, deletable: false },
  };
};

export const mapManagedCollectorInstructions = (raw: unknown): ManagedCollectorInstructions | null => {
  if (!isRecord(raw) || typeof raw.region !== 'string') return null;
  const instructions = raw as unknown as ManagedCollectorInstructions;
  return {
    ...instructions,
    networkId: typeof raw.networkId === 'string' ? raw.networkId : '',
    mode: typeof raw.mode === 'string' ? raw.mode : '',
    documents: Array.isArray(raw.documents) ? instructions.documents : [],
    notes: Array.isArray(raw.notes) ? instructions.notes : [],
  };
};
