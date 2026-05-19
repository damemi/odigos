'use client';

import React from 'react';
import { useServiceMap } from '@/hooks';
import { ServiceMap } from '@odigos/ui-kit/containers';

export default function Page() {
  const { serviceMap } = useServiceMap();

  return <ServiceMap heightToRemove={64} serviceMap={serviceMap} />;
}
