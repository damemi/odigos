import type { SamplingRules } from '@odigos/ui-kit/types';

export interface K8sHealthProbesSamplingConfig {
  enabled: boolean | null;
  keepPercentage: number | null;
}

export type SamplingRuleType = 'noisy' | 'highlyRelevant' | 'costReduction';

export interface SamplingQueryResponse {
  sampling: {
    configs: {
      effective: {
        k8sHealthProbesSampling: K8sHealthProbesSamplingConfig | null;
      };
    };
    rules: SamplingRules[];
  };
}
