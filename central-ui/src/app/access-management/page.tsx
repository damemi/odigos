'use client';

import React, { Suspense, useCallback, useMemo } from 'react';
import { useUserStore } from '@/store';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import type { CurrentUser, UserRole } from '@odigos/ui-kit/types';
import { AccessManagementNext, type AccessTab } from '@odigos/ui-kit/containers';

// The open tab lives in the URL (`?tab=`), so a refresh or a shared link opens on it.
const AccessManagement = () => {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const { user } = useUserStore();
  // Central's roles and the kit's are the same strings.
  const currentUser = useMemo<CurrentUser | null>(() => (user ? { id: user.id, role: user.role as unknown as UserRole } : null), [user]);

  const onTabChange = useCallback(
    (tab: AccessTab) => {
      const params = new URLSearchParams(searchParams.toString());
      params.set('tab', tab);
      router.replace(`${pathname}?${params}`);
    },
    [router, pathname, searchParams],
  );

  return <AccessManagementNext currentUser={currentUser} tab={searchParams.get('tab') ?? undefined} onTabChange={onTabChange} />;
};

// `useSearchParams` needs a Suspense boundary for the production build.
export default function Page() {
  return (
    <Suspense>
      <AccessManagement />
    </Suspense>
  );
}
