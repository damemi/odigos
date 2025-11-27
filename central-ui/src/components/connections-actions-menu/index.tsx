'use client';

import React, { useEffect, useMemo } from 'react';
import styled from 'styled-components';
import Theme from '@odigos/ui-kit/theme';
import { useComputePlatformsCRUD } from '@/hooks';
import { useFilterStore } from '@odigos/ui-kit/store';
import { RefreshIcon, SearchIcon } from '@odigos/ui-kit/icons';
import { Badge, Button, Divider, Dropdown, DropdownOption, DropdownProps, FlexRow, Input, Text } from '@odigos/ui-kit/components';

const Container = styled.div`
  display: flex;
  align-items: center;
  margin: 20px 0;
  padding: 0 16px;
  gap: 8px;
`;

const Title = styled(Text)`
  font-size: 24px;
  white-space: nowrap;
`;

const SearchAndFilterWrapper = styled.div`
  width: 200px;
`;

const PushRight = styled.div`
  margin-left: auto;
`;

export const ConnectionsActionsMenu = () => {
  const theme = Theme.useTheme();
  const { computePlatforms, refetch, loading } = useComputePlatformsCRUD();
  const { searchText, setSearchText, statuses, setStatuses, platformTypes, setPlatformTypes, clearAll } = useFilterStore();

  // cleanup filters on unmount
  useEffect(() => {
    return () => clearAll();
  }, [clearAll]);

  const statusOptions = useMemo(() => {
    const payload: DropdownProps['options'] = [];

    computePlatforms?.forEach(({ status }) => {
      if (!payload.find((opt) => opt.id === status)) {
        payload.push({ id: status, value: status });
      }
    });

    return payload;
  }, [computePlatforms]);

  const typeOptions = useMemo(() => {
    const payload: DropdownProps['options'] = [];

    computePlatforms?.forEach(({ type }) => {
      if (!payload.find((opt) => opt.id === type)) {
        payload.push({ id: type, value: type });
      }
    });

    return payload;
  }, [computePlatforms]);

  return (
    <Container>
      <FlexRow $gap={16}>
        <FlexRow $gap={12}>
          <Title>Connections</Title>
          <Badge label={computePlatforms.length} filled={!!computePlatforms.length} />
        </FlexRow>

        <Divider orientation='vertical' length='20px' margin='0' />

        <SearchAndFilterWrapper>
          <Input placeholder='Search...' icon={SearchIcon} value={searchText} onChange={(e) => setSearchText(e.target.value.toLowerCase())} />
        </SearchAndFilterWrapper>

        <SearchAndFilterWrapper>
          <Dropdown
            disabled={!statusOptions?.length}
            title=''
            placeholder='All Statuses'
            options={statusOptions}
            value={statuses}
            onSelect={(val) => setStatuses([...(statuses || []), val])}
            onDeselect={(val) => setStatuses((statuses || []).filter((opt: DropdownOption) => opt.id !== val.id))}
            showSearch
            required
            isMulti
          />
        </SearchAndFilterWrapper>

        <SearchAndFilterWrapper>
          <Dropdown
            disabled={!typeOptions?.length}
            title=''
            placeholder='All Types'
            options={typeOptions}
            value={platformTypes}
            onSelect={(val) => setPlatformTypes([...(platformTypes || []), val])}
            onDeselect={(val) => setPlatformTypes((platformTypes || []).filter((opt: DropdownOption) => opt.id !== val.id))}
            showSearch
            required
            isMulti
          />
        </SearchAndFilterWrapper>
      </FlexRow>

      <PushRight>
        <Button onClick={() => refetch()} disabled={loading}>
          <RefreshIcon fill={theme.text.primary} />
          {loading ? 'Refreshing...' : 'Refresh Connections'}
        </Button>
      </PushRight>
    </Container>
  );
};
