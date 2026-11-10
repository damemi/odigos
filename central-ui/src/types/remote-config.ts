import type { EffectiveConfigInput } from '@odigos/ui-kit/types';

/** GraphQL `RemoteConfigInput` — fields supported by `updateRemoteConfig` on the proxy. */
export type RemoteConfigInput = Pick<Partial<EffectiveConfigInput>, 'rollout'>;
