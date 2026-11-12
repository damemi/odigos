'use client';

import React from 'react';
import { useProxy } from '@/hooks';
import { PlatformType } from '@odigos/ui-kit/types';
import { ManagedCollectorsNext, PipelineCollectorsNext } from '@odigos/ui-kit/containers';

export default function Page() {
  const { platformType, proxyName, proxyID } = useProxy();

  // Cloud connectors manage a Collector per region.
  if (platformType === PlatformType.Connector) return <ManagedCollectorsNext connectorName={proxyName} connectorId={proxyID} />;

  return (
    <PipelineCollectorsNext
      minSupportedVersion={{
        [PlatformType.K8s]: 1.12,
      }}
    />
  );
}
