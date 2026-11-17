'use client';

import React, { useEffect } from 'react';
import { ROUTES } from '@/utils';
import { useConfig } from '@/hooks';
import { useRouter } from 'next/navigation';
// ui-kit still exports the page as Interrogations.
import { Interrogations as Autopilot } from '@odigos/ui-kit/containers';
import { CenterThis, Loader } from '@odigos/ui-kit/components';

// The nav entry is already hidden when autopilot is disabled, so this guard
// only catches direct navigation to `/autopilot`.
export default function Page() {
  const router = useRouter();
  const { config } = useConfig();

  useEffect(() => {
    if (config && !config.interrogationEnabled) router.replace(ROUTES.OVERVIEW);
  }, [config, router]);

  if (!config?.interrogationEnabled) {
    return (
      <CenterThis style={{ height: '100%' }}>
        <Loader withSpinnerOld scaleSpinnerOld={2} />
      </CenterThis>
    );
  }

  return <Autopilot />;
}
