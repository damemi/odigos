'use client';

import React from 'react';
import { useProxy } from '@/hooks';
import { PlatformType } from '@odigos/ui-kit/types';
import { ManagedCollectors, PipelineCollectors } from '@odigos/ui-kit/containers';

export default function Page() {
  const { platformType, proxyName, proxyID } = useProxy();

  if (platformType === PlatformType.Connector) return <ManagedCollectors connectorName={proxyName} connectorId={proxyID} />;

  return (
    <PipelineCollectors
      minSupportedVersion={{
        [PlatformType.K8s]: 1.12,
      }}
    />
  );
}
