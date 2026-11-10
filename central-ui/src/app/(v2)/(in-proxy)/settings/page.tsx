'use client';

import React, { Suspense } from 'react';
import { useSearchParams } from 'next/navigation';
import { PlatformType } from '@odigos/ui-kit/types';
import { SettingsNext } from '@odigos/ui-kit/containers';

// `?search=` opens the page filtered to that setting (other pages link here for a setting they need).
function SettingsPage() {
  const searchParams = useSearchParams();

  return (
    <SettingsNext
      initialSearch={searchParams.get('search') ?? undefined}
      minSupportedVersion={{
        [PlatformType.K8s]: 1.2,
      }}
    />
  );
}

export default function Page() {
  return (
    <Suspense>
      <SettingsPage />
    </Suspense>
  );
}
