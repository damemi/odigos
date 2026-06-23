import { Role } from '@/types';
import { useUserStore } from '@/store';
import type { FetchedConfig } from '@odigos/ui-kit/types';
import { useApiQuery } from '@odigos/ui-kit/contexts/odigos-api';

export const useConfig = (): { config: FetchedConfig; isReadonly: boolean } => {
  const { user, canEdit } = useUserStore();
  const { data: config } = useApiQuery('GET_CONFIG');

  const isReadonly = user?.role === Role.VIEWER || (!!config?.readonly && !canEdit);

  return { config, isReadonly };
};
