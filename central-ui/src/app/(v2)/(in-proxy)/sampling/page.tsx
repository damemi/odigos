'use client';

import React, { useCallback } from 'react';
import { ROUTES } from '@/utils';
import { useProxy } from '@/hooks';
import { useRouter } from 'next/navigation';
import { PlatformType } from '@odigos/ui-kit/types';
import { SamplingRulesNext } from '@odigos/ui-kit/containers';

export default function Page() {
  const router = useRouter();
  const { platformType } = useProxy();

  // A setting the page needs (e.g. tail sampling, when it's off) opens Settings searched for it. Only Kubernetes
  // clusters have a Settings page in Central.
  const onOpenSettings = useCallback((search: string) => router.push(`${ROUTES.SETTINGS}?${new URLSearchParams({ search })}`), [router]);

  return <SamplingRulesNext onOpenSettings={platformType === PlatformType.K8s ? onOpenSettings : undefined} />;
}
