'use client';

import React, { Suspense } from 'react';
import { useSearchParams } from 'next/navigation';
import { PlatformType } from '@odigos/ui-kit/types';
import { SettingsNext } from '@odigos/ui-kit/containers';

// Settings is being migrated to consume `useOdigosApi()` directly. Once that
// migration lands, this page becomes a one-line `return <Settings />;`.
function SettingsPage() {
  const searchParams = useSearchParams();

  // `?search=` comes from a feature gate's "Open in Settings".
  return (
    <SettingsNext
      minSupportedVersion={{
        [PlatformType.K8s]: 1.2,
      }}
      initialSearch={searchParams.get('search') ?? undefined}
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
