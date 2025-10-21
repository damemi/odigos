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
  const { token } = useUserStore();

  const { refetch } = useQuery(GET_COMPUTE_PLATFORMS, {
    skip: !token,
    fetchPolicy: 'network-only',
  });

  useEffect(() => {
    if (token) {
      // Fire-and-forget; errors are handled globally by errorLink
      refetch().catch(() => {});
    }
  }, [refetch, token]);
}
