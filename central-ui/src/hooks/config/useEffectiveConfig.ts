import { useCallback, useEffect, useState } from 'react';
import { useProxyStore } from '@/store';
import { useRemoteFetch } from '../proxy';
import type { ComputePlatform } from '@/types';
import { GET_EFFECTIVE_CONFIG } from '@/graphql';
import type { EffectiveConfig } from '@odigos/ui-kit/types';

interface FetchedEffectiveConfig {
  effectiveConfig?: EffectiveConfig;
}

// Maps pre-v1.24 flat effective config fields to the current nested structure.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function mapLegacyEffectiveConfig(raw: any): EffectiveConfig | undefined {
  if (!raw) return undefined;

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const mapped: Record<string, any> = { ...raw };

  if ('waspEnabled' in mapped) {
    mapped.wasp = { enabled: mapped.waspEnabled };
    delete mapped.waspEnabled;
  }

  if ('karpenterEnabled' in mapped) {
    mapped.karpenter = { enabled: mapped.karpenterEnabled };
    delete mapped.karpenterEnabled;
  }

  if (typeof mapped.allowConcurrentAgents === 'boolean' || mapped.allowConcurrentAgents === null) {
    mapped.allowConcurrentAgents = { enabled: mapped.allowConcurrentAgents };
  }

  if ('rollbackDisabled' in mapped || 'rollbackGraceTime' in mapped || 'rollbackStabilityWindow' in mapped) {
    mapped.autoRollback = {
      disabled: mapped.rollbackDisabled ?? null,
      graceTime: mapped.rollbackGraceTime ?? null,
      stabilityWindowTime: mapped.rollbackStabilityWindow ?? null,
    };
    delete mapped.rollbackDisabled;
    delete mapped.rollbackGraceTime;
    delete mapped.rollbackStabilityWindow;
  }

  if ('mountMethod' in mapped || 'agentEnvVarsInjectionMethod' in mapped || 'checkDeviceHealthBeforeInjection' in mapped) {
    mapped.instrumentor = {
      mountMethod: mapped.mountMethod ?? null,
      agentEnvVarsInjectionMethod: mapped.agentEnvVarsInjectionMethod ?? null,
      checkDeviceHealthBeforeInjection: mapped.checkDeviceHealthBeforeInjection ?? null,
    };
    delete mapped.mountMethod;
    delete mapped.agentEnvVarsInjectionMethod;
    delete mapped.checkDeviceHealthBeforeInjection;
  }

  return mapped as EffectiveConfig;
}

export const useEffectiveConfig = (computePlatforms: ComputePlatform[] = [], autoFetch?: boolean) => {
  const { proxyID } = useProxyStore();
  const { fetch: fetchEffectiveConfig } = useRemoteFetch<FetchedEffectiveConfig, void>(GET_EFFECTIVE_CONFIG, computePlatforms);

  const [effectiveConfig, setEffectiveConfig] = useState<EffectiveConfig | undefined>(undefined);
  const [effectiveConfigLoading, setEffectiveConfigLoading] = useState(false);

  // `id` lets callers fetch a specific connection's config (e.g. the bulk-edit drawer on /connections,
  // where proxyID from the store is intentionally undefined). Falls back to the active proxyID for
  // in-proxy pages like /settings that call this without args.
  const getEffectiveConfig = useCallback(
    async (id?: string): Promise<EffectiveConfig | undefined> => {
      setEffectiveConfigLoading(true);

      const { data: res } = await fetchEffectiveConfig(undefined, id || proxyID);
      const mapped = mapLegacyEffectiveConfig(res?.effectiveConfig);

      setEffectiveConfigLoading(false);
      setEffectiveConfig(mapped);

      return mapped;
    },
    [fetchEffectiveConfig, proxyID],
  );

  useEffect(() => {
    if (autoFetch && !effectiveConfig) {
      getEffectiveConfig();
    }
  }, [autoFetch, effectiveConfig, getEffectiveConfig]);

  return {
    getEffectiveConfig,
    // Alias matching the naming used by odigos standalone and ui-kit v2 Settings props.
    refetchEffectiveConfig: getEffectiveConfig,
    effectiveConfig,
    effectiveConfigLoading,
  };
};
