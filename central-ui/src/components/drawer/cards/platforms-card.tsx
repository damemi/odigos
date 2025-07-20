import React, { useState, type FC, useMemo } from 'react';
import Theme from '@odigos/ui-kit/theme';
import type { ComputePlatform } from '@/types';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import { MinusCircledIcon, SearchIcon } from '@odigos/ui-kit/icons';
import { DataCardContentColumn, DataCardContentRow } from './styled';
import { DataCard, DataFinger, IconButton, Input, NoDataFound } from '@odigos/ui-kit/components';

interface PlatformsCardProps {
  formData: {
    computePlatforms?: ComputePlatform[];
  };
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: string, value: any) => void;
}

const PlatformsCard: FC<PlatformsCardProps> = ({ formData, handleFormChange }) => {
  const theme = Theme.useTheme();

  const [search, setSearch] = useState('');
  const filteredPlatforms = useMemo(() => formData.computePlatforms?.filter(({ name }) => name.toLowerCase().includes(search.toLowerCase())), [formData.computePlatforms, search]);

  const platformsCount = formData.computePlatforms?.length || 0;
  const filteredPlatformsCount = filteredPlatforms?.length || 0;

  return (
    <DataCard title='Compute Platforms' titleBadge={platformsCount} action={<Input icon={SearchIcon} placeholder='Search...' value={search} onChange={(e) => setSearch(e.target.value)} />}>
      {!filteredPlatformsCount ? (
        <DataCardContentRow $justifyContent='center'>
          <NoDataFound subTitle={search ? undefined : ''} />
        </DataCardContentRow>
      ) : (
        <DataCardContentColumn>
          {filteredPlatforms?.map((cp) => (
            <DataFinger
              key={`platform-${cp.id}`}
              title={cp.name}
              subTitle={cp.type}
              icons={[getPlatformIcon(cp.type)]}
              onClick={() => {}}
              renderActions={() => (
                <IconButton
                  onClick={() =>
                    handleFormChange(
                      'computePlatforms',
                      formData.computePlatforms?.filter(({ id }) => id !== cp.id),
                    )
                  }
                  tooltip='Remove compute platform'
                >
                  <MinusCircledIcon fill={theme.text.error} />
                </IconButton>
              )}
            />
          ))}
        </DataCardContentColumn>
      )}
    </DataCard>
  );
};

export { PlatformsCard, type PlatformsCardProps };
