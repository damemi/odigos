'use client';

import React from 'react';
import { PlatformType } from '@odigos/ui-kit/types';
import { PipelineCollectorsNext } from '@odigos/ui-kit/containers';

export default function Page() {
  // PLAT-1666: shadcn version of the collectors page (swap back to `PipelineCollectors` to compare).
  return (
    <PipelineCollectorsNext
      minSupportedVersion={{
        [PlatformType.K8s]: 1.12,
      }}
    />
  );
}
