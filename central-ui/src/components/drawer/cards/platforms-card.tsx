import React, { useState, type FC, useMemo } from 'react';
import Theme from '@odigos/ui-kit/theme';
import { useConnectionsCRUD } from '@/hooks';
import type { ComputePlatform } from '@/types';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import { MinWidthWide, Relative } from '@/components/styled';
import { DataCardContentColumn, DataCardContentRow } from './styled';
import { MinusCircledIcon, PlusIcon, SearchIcon } from '@odigos/ui-kit/icons';
import { DataCard, DataFinger, Dropdown, IconButton, Input, NoDataFound, PopupForm } from '@odigos/ui-kit/components';

interface PlatformsCardProps {
  formData: {
    computePlatforms?: ComputePlatform[];
  };
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: string, value: any) => void;
}

const PlatformsCard: FC<PlatformsCardProps> = ({ formData, handleFormChange }) => {
  const theme = Theme.useTheme();
  const { connections } = useConnectionsCRUD();

  const [isAdd, setIsAdd] = useState(false);
  const toggleAdd = () => setIsAdd((prev) => !prev);
  const { formData: tempFormData, handleFormChange: handleTempFormChange, resetFormData: resetTempFormData } = useGenericForm(formData);

  const [search, setSearch] = useState('');
  const filteredPlatforms = useMemo(() => formData.computePlatforms?.filter(({ name }) => name.toLowerCase().includes(search.toLowerCase())), [formData.computePlatforms, search]);

  const platformsCount = formData.computePlatforms?.length || 0;
  const filteredPlatformsCount = filteredPlatforms?.length || 0;

  return (
    <DataCard
      title='Compute Platforms'
      titleBadge={platformsCount}
      action={
        <>
          <Input icon={SearchIcon} placeholder='Search...' value={search} onChange={(e) => setSearch(e.target.value)} />

          <Relative>
            <IconButton size={32} onClick={toggleAdd} withBorder>
              <PlusIcon />
            </IconButton>

            <PopupForm
              flipX
              clientX={36}
              clientY={0}
              isOpen={isAdd}
              title='Add compute platforms from the list'
              onClose={() => {
                resetTempFormData();
                toggleAdd();
              }}
              onSave={() => {
                handleFormChange('computePlatforms', tempFormData.computePlatforms);
                toggleAdd();
              }}
            >
              <MinWidthWide>
                <Dropdown
                  showSearch
                  isMulti
                  options={connections.map((x) => ({ id: x.id, value: x.name }))}
                  value={tempFormData.computePlatforms?.map((x) => ({ id: x.id, value: x.name }))}
                  onSelect={({ id }) => {
                    const fullConnection = connections.find((connection) => connection.id === id);
                    if (fullConnection) {
                      handleTempFormChange('computePlatforms', [...(tempFormData.computePlatforms || []), fullConnection]);
                    }
                  }}
                  onDeselect={({ id }) => {
                    handleTempFormChange(
                      'computePlatforms',
                      tempFormData.computePlatforms?.filter((x) => x.id !== id),
                    );
                  }}
                />
              </MinWidthWide>
            </PopupForm>
          </Relative>
        </>
      }
    >
      {!filteredPlatformsCount ? (
        <DataCardContentRow $justifyContent='center'>
          <NoDataFound subTitle={search ? undefined : ''} />
        </DataCardContentRow>
      ) : (
        <DataCardContentColumn>
          {filteredPlatforms?.map((x) => (
            <DataFinger
              key={`platform-${x.id}`}
              title={x.name}
              subTitle={x.type}
              icons={[getPlatformIcon(x.type)]}
              onClick={() => {}}
              renderActions={() => (
                <IconButton
                  tooltip='Remove compute platform'
                  onClick={() => {
                    handleFormChange(
                      'computePlatforms',
                      formData.computePlatforms?.filter(({ id }) => id !== x.id),
                    );
                  }}
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
