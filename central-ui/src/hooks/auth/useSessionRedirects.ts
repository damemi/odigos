'use client';

import { useEffect } from 'react';
import { useAuth } from './useAuth';
import { ROUTES } from '@/utils';
import { useUserStore } from '@/store';
import { usePathname, useRouter } from 'next/navigation';
import { useValidateSession } from './useValidateSession';

/**
 * Redirects based on presence of a persisted auth token.
 * - If user is on `/signin` and has a token, redirect to `/connections`.
 * - If user is on `/` and has a token, redirect to `/connections`; otherwise to `/signin`.
 */
export function useSessionRedirects() {
  const router = useRouter();
  const pathname = usePathname();
  const { token, clearAuth, user } = useUserStore();
  const { systemConfig, systemConfigLoading } = useAuth();

  // Validate token in background when present; global errorLink will clean up on failure
  useValidateSession();

  useEffect(() => {
    const hasToken = !!token && /^[\x20-\x7E]*$/.test(token);

    if (!!token && !hasToken) {
      clearAuth();
      router.push(ROUTES.SIGN_IN);
      return;
    }

    if (pathname === ROUTES.SIGN_IN && hasToken && !user?.needsPasswordChange && !systemConfigLoading && systemConfig?.isInitialized) {
      router.push(ROUTES.CONNECTIONS);
    } else if (pathname === ROUTES.ROOT) {
      router.push(ROUTES.SIGN_IN);
    }
  }, [pathname, router, token, clearAuth, user, systemConfig, systemConfigLoading]);
}
