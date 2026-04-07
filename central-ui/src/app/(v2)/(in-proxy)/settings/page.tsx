'use client';

import React, { useCallback } from 'react';
import { Settings } from '@odigos/ui-kit/containers/v2';
import type { LocalUiConfigInput } from '@odigos/ui-kit/types';
import { useConfigYamls, useEffectiveConfig, useUpdateLocalUiConfig } from '@/hooks';

export default function Page() {
  const { configYamls, configYamlsLoading } = useConfigYamls();
  const { updateLocalUiConfig, loading: saveLoading } = useUpdateLocalUiConfig();
  const { effectiveConfig, effectiveConfigLoading, getEffectiveConfig } = useEffectiveConfig([], true);

  const onSave = useCallback(
    async (config: LocalUiConfigInput) => {
      await updateLocalUiConfig(config);
      await getEffectiveConfig();
    },
    [updateLocalUiConfig, getEffectiveConfig],
  );

  return (
    <Settings
      pageHeightOffset={62}
      minSupportedVersion={1.2}
      configYamls={configYamls}
      configYamlsLoading={configYamlsLoading}
      effectiveConfig={effectiveConfig}
      effectiveConfigLoading={effectiveConfigLoading}
      onSave={onSave}
      saveLoading={saveLoading}
    />
  );
}
