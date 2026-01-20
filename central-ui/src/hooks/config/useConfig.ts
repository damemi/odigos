import { useEffect } from 'react';
import { GET_CONFIG } from '@/graphql';
import { useUserStore } from '@/store';
import { useRemoteFetch } from '../proxy';
import { Role, type FetchedConfig } from '@/types';

export const useConfig = () => {
  const { user, isAdmin } = useUserStore();
  const { called, data, fetch } = useRemoteFetch<{ config?: FetchedConfig }>(GET_CONFIG);

  useEffect(() => {
    if (!called) fetch();
  }, [called]);

  const config = data?.config;
  const isReadonly = user?.role === Role.VIEWER || (data?.config?.readonly && !isAdmin);

  return {
    config,
    isReadonly,
  };
};
