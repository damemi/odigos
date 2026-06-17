import { useUserStore } from '@/store';
import { Role } from '@/types';
import { useOdigosApi } from '@odigos/ui-kit/contexts/odigos-api';

/**
 * Fetches the active proxy's `FetchedConfig` (tier / readonly /
 * installation*) via the kit's typed `GET_CONFIG` slot and combines it
 * with the local user's role to derive `isReadonly`.
 *
 * Apollo handles caching + reactive re-render — there's nothing to
 * narrow at the call site because the kit's slot is bare-typed
 * (`Operation<FetchedConfig, undefined>`).
 */
export const useConfig = () => {
  const { user, isAdmin } = useUserStore();
  const { data: config } = useOdigosApi().configApi.useEffectiveConfig();

  const isReadonly = user?.role === Role.VIEWER || (!!config?.readonly && !isAdmin);

  return { config, isReadonly };
};
