import React, { useState, type FC, useMemo } from 'react';
import type { User } from '@/types';
import Theme from '@odigos/ui-kit/theme';
import { DataCardContent } from './styled';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import { MinusCircledIcon, SearchIcon } from '@odigos/ui-kit/icons';
import { DataCard, DataFinger, IconButton, Input, NoDataFound } from '@odigos/ui-kit/components';

interface PlatformCardProps {
  formData: Pick<User, 'computePlatforms'>;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: string, value: any) => void;
}

const PlatformCard: FC<PlatformCardProps> = ({ formData, handleFormChange }) => {
  const theme = Theme.useTheme();

  const [search, setSearch] = useState('');
  const filteredPlatforms = useMemo(() => formData.computePlatforms?.filter((cp) => cp.name.toLowerCase().includes(search.toLowerCase())), [formData.computePlatforms, search]);

  const platformsCount = formData.computePlatforms?.length || 0;
  const filteredPlatformsCount = filteredPlatforms?.length || 0;

  return (
    <DataCard title='Compute Platforms Access' titleBadge={platformsCount} action={<Input icon={SearchIcon} placeholder='Search...' value={search} onChange={(e) => setSearch(e.target.value)} />}>
      <DataCardContent $justifyContent={!filteredPlatformsCount ? 'center' : ''}>
        {!filteredPlatformsCount ? (
          <NoDataFound subTitle={search ? undefined : ''} />
        ) : (
          filteredPlatforms?.map((cp) => (
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
                  tooltip='Remove user from compute platform'
                >
                  <MinusCircledIcon fill={theme.text.error} />
                </IconButton>
              )}
            />
          ))
        )}
      </DataCardContent>
    </DataCard>
  );
};

export { PlatformCard, type PlatformCardProps };
