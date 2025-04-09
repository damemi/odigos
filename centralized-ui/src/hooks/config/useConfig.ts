import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { GET_CONFIG } from '@/graphql';
import { Tier } from '@odigos/ui-kit/types';
import type { FetchedConfig } from '@/types';

export const useConfig = () => {
  const { data, fetch } = useProxy<{ config?: FetchedConfig }>(GET_CONFIG);

  useEffect(() => {
    if (!data?.config) fetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const config = data?.config;
  const isReadonly = data?.config?.readonly || false;
  const isCommunity = (config?.tier && [Tier.Community].includes(config.tier)) || false;
  const isEnterprise = (config?.tier && [Tier.Onprem].includes(config.tier)) || false;

  return {
    config,
    isReadonly,
    isCommunity,
    isEnterprise,
  };
};
