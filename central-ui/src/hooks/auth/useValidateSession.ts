'use client';

import { useEffect } from 'react';
import { useQuery } from '@apollo/client';
import { useUserStore } from '@/store';
import { GET_COMPUTE_PLATFORMS } from '@/graphql';

/**
 * Triggers a lightweight authenticated query to validate the current token.
 * Apollo's errorLink handles invalid/expired tokens by clearing auth and redirecting.
 */
export function useValidateSession() {
  const { accessToken } = useUserStore();

  const { refetch } = useQuery(GET_COMPUTE_PLATFORMS, {
    skip: !accessToken,
    fetchPolicy: 'network-only',
  });

  useEffect(() => {
    if (accessToken) {
      // Fire-and-forget; errors are handled globally by errorLink
      refetch().catch(() => {});
    }
  }, [refetch, accessToken]);
}
