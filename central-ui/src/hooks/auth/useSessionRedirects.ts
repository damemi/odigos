'use client';

import { useEffect } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { ROUTES } from '@/utils';
import { useValidateSession } from './useValidateSession';
import { useUserStore } from '@/store';

/**
 * Redirects based on presence of a persisted auth token.
 * - If user is on `/signin` and has a token, redirect to `/connections`.
 * - If user is on `/` and has a token, redirect to `/connections`; otherwise to `/signin`.
 */
export function useSessionRedirects() {
  const router = useRouter();
  const pathname = usePathname();
  const { token, clearAuth } = useUserStore();

  // Validate token in background when present; global errorLink will clean up on failure
  useValidateSession();

  useEffect(() => {
    const hasToken = !!token && /^[\x20-\x7E]*$/.test(token);

    if (!!token && !hasToken) {
      clearAuth();
      router.push(ROUTES.SIGN_IN);
      return;
    }

    if (pathname === ROUTES.ROOT) {
      if (hasToken) router.push(ROUTES.CONNECTIONS);
      else router.push(ROUTES.SIGN_IN);
      return;
    }

    if (pathname === ROUTES.SIGN_IN && hasToken) {
      router.push(ROUTES.CONNECTIONS);
    }
  }, [pathname, router, token, clearAuth]);
}
