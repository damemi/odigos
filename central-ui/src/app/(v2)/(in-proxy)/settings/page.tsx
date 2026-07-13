'use client';

import React from 'react';
import { PlatformType } from '@odigos/ui-kit/types';
import { Settings } from '@odigos/ui-kit/containers';

export default function Page() {
  return (
    <Settings
      minSupportedVersion={{
        [PlatformType.K8s]: 1.2,
      }}
    />
  );
}
