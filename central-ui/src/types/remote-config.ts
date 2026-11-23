import type { EffectiveConfigInput } from '@odigos/ui-kit/types';

/** GraphQL `RemoteConfigInput` — fields supported by `updateRemoteConfig` on the proxy. */
export type RemoteConfigInput = Pick<Partial<EffectiveConfigInput>, 'rollout'> & {
  /** The gateway's https proxy (v1.41+): an empty address means no proxy, and an empty `noProxy` means no bypass list, over Helm's. */
  collectorGateway?: { httpsProxyAddress?: string; noProxy?: string[] };
};
