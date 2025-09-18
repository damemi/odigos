import React, { useState, type FC, useMemo } from 'react';
import Theme from '@odigos/ui-kit/theme';
import type { ComputePlatform } from '@/types';
import { useComputePlatformsCRUD } from '@/hooks';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import { MinWidthWide, Relative } from '@/components/styled';
import { DataCardContentColumn, DataCardContentRow } from './styled';
import { MinusCircledIcon, PlusIcon, SearchIcon } from '@odigos/ui-kit/icons';
import { DataCard, DataFinger, Dropdown, IconButton, Input, NoDataFound, PopupForm } from '@odigos/ui-kit/components';
import { useUserStore } from '@/store';

interface FormData {
  addComputePlatformIds: string[];
  removeComputePlatformIds: string[];
}

interface PlatformsCardProps {
  currentComputePlatforms: ComputePlatform[];
  formData: FormData;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: keyof FormData, value: any) => void;
}

const PlatformsCard: FC<PlatformsCardProps> = ({ currentComputePlatforms, formData, handleFormChange }) => {
  const theme = Theme.useTheme();
  const { isAdmin } = useUserStore();
  const { computePlatforms } = useComputePlatformsCRUD();

  const [search, setSearch] = useState('');
  const [isAdd, setIsAdd] = useState(false);
  const toggleAdd = () => setIsAdd((prev) => !prev);

  const filteredPlatforms = useMemo(() => {
    const arr =
      currentComputePlatforms.filter(({ id, name }) => {
        return !formData.removeComputePlatformIds.includes(id) && name.toLowerCase().includes(search.toLowerCase());
      }) || [];

    formData.addComputePlatformIds.forEach((id) => {
      const user = computePlatforms.find((user) => user.id === id);
      const exists = arr.find((user) => user.id === id);
      if (user && !exists) {
        arr.push(user);
      }
    });

    return arr;
  }, [currentComputePlatforms, search, computePlatforms, formData]);

  return (
    <DataCard
      title='Compute Platforms'
      titleBadge={currentComputePlatforms.length}
      action={
        <>
          <Input icon={SearchIcon} placeholder='Search...' value={search} onChange={(e) => setSearch(e.target.value)} />

          <Relative>
            {isAdmin && (
              <IconButton size={32} onClick={toggleAdd} withBorder>
                <PlusIcon />
              </IconButton>
            )}

            <PopupForm
              flipX
              clientX={36}
              clientY={0}
              isOpen={isAdd}
              title='Add compute platforms from the list'
              onClose={() => {
                handleFormChange('addComputePlatformIds', []);
                toggleAdd();
              }}
              onSave={() => {
                toggleAdd();
              }}
            >
              <MinWidthWide>
                <Dropdown
                  showSearch
                  isMulti
                  options={computePlatforms.map((cp) => ({ id: cp.id, value: cp.name }))}
                  value={formData.addComputePlatformIds.map((id) => {
                    const cp = computePlatforms.find((cp) => cp.id === id);
                    return { id, value: cp?.name || cp?.id };
                  })}
                  onSelect={({ id }) => {
                    if (id) {
                      if (formData.removeComputePlatformIds.includes(id)) {
                        const newRemoveComputePlatformIds = formData.removeComputePlatformIds.filter((str) => str !== id);
                        handleFormChange('removeComputePlatformIds', newRemoveComputePlatformIds);
                      }

                      const foundAdd = formData.addComputePlatformIds.find((str) => str === id);
                      const foundCurrent = currentComputePlatforms.find((cp) => cp.id === id);
                      if (!foundAdd && !foundCurrent) {
                        const newAddComputePlatformIds = [...formData.addComputePlatformIds, id];
                        handleFormChange('addComputePlatformIds', newAddComputePlatformIds);
                      }
                    }
                  }}
                  onDeselect={({ id }) => {
                    if (id) {
                      if (formData.addComputePlatformIds.includes(id)) {
                        const newAddComputePlatformIds = formData.addComputePlatformIds.filter((str) => str !== id);
                        handleFormChange('addComputePlatformIds', newAddComputePlatformIds);
                      }
                      // note: do not handle the 'remove' list here, it's not needed
                    }
                  }}
                />
              </MinWidthWide>
            </PopupForm>
          </Relative>
        </>
      }
    >
      {!filteredPlatforms.length ? (
        <DataCardContentRow $justifyContent='center'>
          <NoDataFound subTitle={search ? undefined : ''} />
        </DataCardContentRow>
      ) : (
        <DataCardContentColumn>
          {filteredPlatforms?.map(({ id, name, type }) => (
            <DataFinger
              key={`platform-${id}`}
              title={name}
              subTitle={type}
              icons={[getPlatformIcon(type)]}
              renderActions={
                isAdmin
                  ? () => (
                      <IconButton
                        tooltip='Remove compute platform'
                        onClick={() => {
                          if (formData.addComputePlatformIds.includes(id)) {
                            const newAddComputePlatformIds = formData.addComputePlatformIds.filter((str) => str !== id);
                            handleFormChange('addComputePlatformIds', newAddComputePlatformIds);
                          }

                          const foundRemove = formData.removeComputePlatformIds.find((str) => str === id);
                          const foundCurrent = currentComputePlatforms.find((cp) => cp.id === id);
                          if (!foundRemove && foundCurrent) {
                            const newRemoveComputePlatformIds = formData.removeComputePlatformIds.filter((str) => str !== id);
                            handleFormChange('removeComputePlatformIds', newRemoveComputePlatformIds);
                          }
                        }}
                      >
                        <MinusCircledIcon fill={theme.text.error} />
                      </IconButton>
                    )
                  : undefined
              }
            />
          ))}
        </DataCardContentColumn>
      )}
    </DataCard>
  );
};

export { PlatformsCard, type PlatformsCardProps };
