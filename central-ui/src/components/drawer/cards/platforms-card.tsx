import React, { useMemo, useState, type FC } from 'react';
import { useUserStore } from '@/store';
import { useTheme } from 'styled-components';
import type { ComputePlatform } from '@/types';
import type { SVG } from '@odigos/ui-kit/types';
import { useComputePlatformsCRUD } from '@/hooks';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import { MinWidthWide, Relative } from '@/components/styled';
import { PopupForm, Tooltip } from '@odigos/ui-kit/components';
import { MinusCircledIcon, PlusIcon } from '@odigos/ui-kit/icons';
import { Badge, DropDown, IconButton, IconButtonSize, NoData, Search, Typography, TypographyColor, TypographySize } from '@odigos/ui-kit/components/v2';
import { DataCardContentColumn, DataCardContentRow, ListCard, ListCardHeader, ListCardHeaderLeft, ListCardHeaderRight, ListRow, ListRowMain, ListRowText } from './styled';

interface FormData {
  addComputePlatformIds: string[];
  removeComputePlatformIds: string[];
}

interface PlatformsCardProps {
  currentComputePlatforms: ComputePlatform[];
  formData: FormData;
  handleFormChange: (key: keyof FormData, value: string[]) => void;
}

const PlatformsCard: FC<PlatformsCardProps> = ({ currentComputePlatforms, formData, handleFormChange }) => {
  const theme = useTheme();
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
      const cp = computePlatforms.find((cp) => cp.id === id);
      const exists = arr.find((cp) => cp.id === id);
      if (cp && !exists) {
        arr.push(cp);
      }
    });

    return arr;
  }, [currentComputePlatforms, search, computePlatforms, formData]);

  const RemoveIcon = useMemo<SVG>(() => {
    const Cmp: SVG = (props) => <MinusCircledIcon fill={theme.text.error} {...props} />;
    return Cmp;
  }, [theme]);

  return (
    <ListCard>
      <ListCardHeader>
        <ListCardHeaderLeft>
          <Typography>Compute Platforms</Typography>
          <Badge label={currentComputePlatforms.length} />
        </ListCardHeaderLeft>
        <ListCardHeaderRight>
          <Search value={search} onChange={setSearch} placeholder='Search...' />
          <Relative>
            {isAdmin && <IconButton icon={PlusIcon} size={IconButtonSize.M} onClick={toggleAdd} />}

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
                <DropDown
                  withSearch
                  withMultiSelect
                  options={computePlatforms.map((cp) => ({ id: cp.id, label: cp.name }))}
                  values={formData.addComputePlatformIds}
                  setValues={(ids) => {
                    const filtered = ids.filter((id) => !currentComputePlatforms.find((cp) => cp.id === id));
                    handleFormChange('addComputePlatformIds', filtered);

                    const removed = formData.removeComputePlatformIds.filter((id) => !ids.includes(id));
                    if (removed.length !== formData.removeComputePlatformIds.length) {
                      handleFormChange('removeComputePlatformIds', removed);
                    }
                  }}
                />
              </MinWidthWide>
            </PopupForm>
          </Relative>
        </ListCardHeaderRight>
      </ListCardHeader>

      {!filteredPlatforms.length ? (
        <DataCardContentRow $justifyContent='center'>
          <NoData subTitle={search ? undefined : ''} />
        </DataCardContentRow>
      ) : (
        <DataCardContentColumn>
          {filteredPlatforms.map(({ id, name, type }) => {
            const PlatformIcon = getPlatformIcon(type);
            return (
              <ListRow key={`platform-${id}`}>
                <ListRowMain>
                  <PlatformIcon size={16} />
                  <ListRowText>
                    <Typography size={TypographySize.S}>{name}</Typography>
                    <Typography size={TypographySize.XS} color={TypographyColor.Secondary}>
                      {type}
                    </Typography>
                  </ListRowText>
                </ListRowMain>
                {isAdmin && (
                  <Tooltip text='Remove compute platform'>
                    <IconButton
                      icon={RemoveIcon}
                      size={IconButtonSize.S}
                      onClick={() => {
                        if (formData.addComputePlatformIds.includes(id)) {
                          const newAddComputePlatformIds = formData.addComputePlatformIds.filter((str) => str !== id);
                          handleFormChange('addComputePlatformIds', newAddComputePlatformIds);
                        }

                        const foundRemove = formData.removeComputePlatformIds.find((str) => str === id);
                        const foundCurrent = currentComputePlatforms.find((cp) => cp.id === id);
                        if (!foundRemove && foundCurrent) {
                          const newRemoveComputePlatformIds = [...formData.removeComputePlatformIds, id];
                          handleFormChange('removeComputePlatformIds', newRemoveComputePlatformIds);
                        }
                      }}
                    />
                  </Tooltip>
                )}
              </ListRow>
            );
          })}
        </DataCardContentColumn>
      )}
    </ListCard>
  );
};

export { PlatformsCard, type PlatformsCardProps };
