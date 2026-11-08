'use client';

import React from 'react';
import { useProxy } from '@/hooks';
import { PlatformType } from '@odigos/ui-kit/types';
import { ManagedCollectors, PipelineCollectorsNext } from '@odigos/ui-kit/containers';

export default function Page() {
  const { platformType, proxyName, proxyID } = useProxy();

  // Cloud connectors keep the managed collectors page: it has no redesigned version yet.
  if (platformType === PlatformType.Connector) return <ManagedCollectors connectorName={proxyName} connectorId={proxyID} />;

  return (
    <PipelineCollectorsNext
      minSupportedVersion={{
        [PlatformType.K8s]: 1.12,
      }}
    />
  );
}
