'use client';

import React, { Suspense } from 'react';
import { ROUTES } from '@/utils';
import { ProfilingV2 } from '@odigos/ui-kit/containers';
import { useRouter, useSearchParams } from 'next/navigation';

// The selected source (and one that couldn't be profiled) live in the URL, so the Overview can link to them.
function ProfilingPage() {
  const router = useRouter();
  const searchParams = useSearchParams();

  return (
    <ProfilingV2
      unavailableSourceKey={searchParams.get('unavailableSource')}
      onUnavailableSourceDismiss={() => router.replace(ROUTES.PROFILING)}
      selectedSourceKey={searchParams.get('source')}
      onSelectedSourceKeyChange={(key) => router.push(key ? `${ROUTES.PROFILING}?${new URLSearchParams({ source: key })}` : ROUTES.PROFILING)}
    />
  );
}

export default function Page() {
  return (
    <Suspense>
      <ProfilingPage />
    </Suspense>
  );
}
