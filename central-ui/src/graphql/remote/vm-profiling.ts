import type { EnableProfilingResult, HotFunctionsResult, ProfilingSlots, SourceProfilingResult, WorkloadId } from '@odigos/ui-kit/types';

type VmHotFunctionsEnvelope = {
  profileHotFunctions?: HotFunctionsResult | null;
  profileHotFunctionsV2?: HotFunctionsResult | null;
};

/** Agents-api `profileHotFunctions` envelope → kit `HotFunctionsResult`. */
export const mapVmHotFunctions = (data: unknown): HotFunctionsResult | undefined => {
  const env = data as VmHotFunctionsEnvelope | null | undefined;
  return env?.profileHotFunctions ?? env?.profileHotFunctionsV2 ?? undefined;
};

/** Agents-api `clearProfileCache` envelope → bare boolean. */
export const mapVmClearProfileCache = (data: unknown): boolean | undefined => {
  const env = data as { clearProfileCache?: boolean; clearProfileCacheV2?: boolean } | null | undefined;
  return env?.clearProfileCache ?? env?.clearProfileCacheV2;
};

/** Resolve the agents-api service key from a kit workload id (+ optional serviceName). */
export const vmProfilingService = (vars: WorkloadId & { serviceName?: string | null }): string => vars.serviceName || vars.name;

/** Flatten VM `profilingSlots { cache {…} }` into the kit's flat `ProfilingSlots`. */
export const mapVmProfilingSlots = (data: unknown): ProfilingSlots | undefined => {
  const env = data as
    | {
        profilingSlots?: {
          activeKeys?: string[];
          keysWithData?: string[];
          cache?: {
            totalBytes?: number;
            slotMaxBytes?: number;
            maxSlots?: number;
            maxTotalBytesBudget?: number;
            slotTtlSeconds?: number;
          } | null;
        } | null;
      }
    | null
    | undefined;
  const slots = env?.profilingSlots;
  if (!slots) return undefined;
  const cache = slots.cache;
  return {
    activeKeys: slots.activeKeys ?? [],
    keysWithData: slots.keysWithData ?? [],
    totalBytesUsed: cache?.totalBytes ?? 0,
    slotMaxBytes: cache?.slotMaxBytes ?? 0,
    maxSlots: cache?.maxSlots ?? 0,
    maxTotalBytesBudget: cache?.maxTotalBytesBudget ?? 0,
    slotTtlSeconds: cache?.slotTtlSeconds ?? 0,
  };
};

export const mapVmSourceProfile = (data: unknown): SourceProfilingResult | undefined => {
  const env = data as { sourceProfile?: SourceProfilingResult | null } | null | undefined;
  return env?.sourceProfile ?? undefined;
};

export const mapVmEnableSourceProfiling = (data: unknown): EnableProfilingResult | undefined => {
  const env = data as { enableSourceProfiling?: EnableProfilingResult | null } | null | undefined;
  return env?.enableSourceProfiling ?? undefined;
};
