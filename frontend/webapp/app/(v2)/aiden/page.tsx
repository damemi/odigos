'use client';

import React, { useEffect } from 'react';
import { API, ROUTES } from '@/utils';
import { useConfig } from '@/hooks';
import { useRouter } from 'next/navigation';
import { Aiden } from '@odigos/ui-kit/containers';
import { CenterThis, Loader } from '@odigos/ui-kit/components';

const aidenWsUrl = () => `${API.BACKEND_HTTP_ORIGIN.replace(/^http/, 'ws')}/api/aiden/ws`;

export default function Page() {
  const router = useRouter();
  const { config } = useConfig();

  useEffect(() => {
    if (config && !config.aidenEnabled) router.replace(ROUTES.OVERVIEW);
  }, [config, router]);

  if (!config?.aidenEnabled) {
    return (
      <CenterThis style={{ height: '100%' }}>
        <Loader withSpinnerOld scaleSpinnerOld={2} />
      </CenterThis>
    );
  }

  return <Aiden wsUrl={aidenWsUrl()} />;
}
