import { useEffect, useMemo } from 'react';
import { useConfig } from '../config';
import { useRemoteFetch } from '../proxy';
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

  const { called, loading, data, fetch: queryAll } = useRemoteFetch<{ computePlatform?: { apiTokens?: TokenPayload[] } }>(GET_TOKENS);
  const { fetch: mutateUpdate } = useRemoteFetch<{ computePlatform?: { apiTokens?: TokenPayload[] } }>(UPDATE_TOKEN);

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
    if (!called) queryAll();
  }, [called]);

  const tokens = useMemo(() => data?.computePlatform?.apiTokens || [], [data?.computePlatform?.apiTokens?.length]);

  return {
    loading,
    tokens,
    updateToken,
  };
};
