import { useProxy } from '../proxy';
import { useConfig } from './useConfig';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { FetchedOdigosConfig, OdigosConfigInput } from '@/types';
import { GET_ODIGOS_CONFIG, UPDATE_ODIGOS_CONFIG } from '@/graphql';
import { Crud, OdigosConfig, StatusType } from '@odigos/ui-kit/types';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { mapFetchedOdigosConfig, mapOdigosConfigToInput } from '@/utils';

export const useOdigosConfigCRUD = () => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();

  const { fetch: queryOdigosConfig } = useProxy<{ odigosConfig: FetchedOdigosConfig }>(GET_ODIGOS_CONFIG);
  const { fetch: mutateUpdateOdigosConfig } = useProxy<{ updateOdigosConfig: boolean }, { odigosConfig: OdigosConfigInput }>(UPDATE_ODIGOS_CONFIG);

  const fetchOdigosConfig = async () => {
    const { data } = await queryOdigosConfig();
    if (data?.odigosConfig) {
      return mapFetchedOdigosConfig(data.odigosConfig);
    }
  };

  const updateOdigosConfig = async (payload: Partial<OdigosConfig>) => {
    if (isReadonly) {
      addNotification({ type: StatusType.Warning, title: DISPLAY_TITLES.READONLY, message: FORM_ALERTS.READONLY_WARNING, hideFromHistory: true });
    } else {
      const { data } = await mutateUpdateOdigosConfig({ odigosConfig: mapOdigosConfigToInput(payload) });
      if (data?.updateOdigosConfig) {
        addNotification({ type: StatusType.Success, title: Crud.Update, message: 'Odigos configuration updated successfully' });
      }
    }
  };

  return {
    fetchOdigosConfig,
    updateOdigosConfig,
  };
};
