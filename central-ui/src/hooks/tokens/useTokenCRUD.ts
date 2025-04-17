import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { useConfig } from '../config';
import { GET_TOKENS, UPDATE_TOKEN } from '@/graphql';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { Crud, StatusType, type TokenPayload } from '@odigos/ui-kit/types';

export const useTokenCRUD = () => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();

  const notifyUser = (type: StatusType, title: string, message: string, hideFromHistory?: boolean) => {
    addNotification({ type, title, message, hideFromHistory });
  };

  const { data, loading, fetch: queryAll } = useProxy<{ computePlatform?: { apiTokens?: TokenPayload[] } }>(GET_TOKENS);
  const { fetch: mutateUpdate } = useProxy<{ computePlatform?: { apiTokens?: TokenPayload[] } }>(UPDATE_TOKEN);

  const updateToken = async (token: string) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, true);
    } else {
      await mutateUpdate({ token });
      notifyUser(StatusType.Success, Crud.Update, 'API Token updated');
      queryAll();
    }
  };

  useEffect(() => {
    if (!data?.computePlatform?.apiTokens) queryAll();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return {
    loading,
    tokens: data?.computePlatform?.apiTokens || [],
    updateToken,
  };
};
