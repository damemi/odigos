'use client';

import React, { useEffect, useMemo } from 'react';
import styled from 'styled-components';
import { useConnectionsCRUD } from '@/hooks';
import { SearchIcon } from '@odigos/ui-kit/icons';
import { useFilterStore } from '@odigos/ui-kit/store';
import { Badge, Divider, Dropdown, DropdownOption, DropdownProps, FlexRow, Input, Text } from '@odigos/ui-kit/components';

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

export const ConnectionsActionsMenu = () => {
  const { connections } = useConnectionsCRUD();
  const { searchText, setSearchText, statuses, setStatuses, platformTypes, setPlatformTypes, clearAll } = useFilterStore();

  // cleanup filters on unmount
  useEffect(() => {
    return () => clearAll();
  }, [clearAll]);

  const statusOptions = useMemo(() => {
    const payload: DropdownProps['options'] = [];

    connections?.forEach(({ status }) => {
      if (!payload.find((opt) => opt.id === status)) {
        payload.push({ id: status, value: status });
      }
    });

    return payload;
  }, [connections]);

  const typeOptions = useMemo(() => {
    const payload: DropdownProps['options'] = [];

    connections?.forEach(({ type }) => {
      if (!payload.find((opt) => opt.id === type)) {
        payload.push({ id: type, value: type });
      }
    });

    return payload;
  }, [connections]);

  return (
    <Container>
      <FlexRow $gap={16}>
        <FlexRow $gap={12}>
          <Title>Compute platforms</Title>
          <Badge label={connections.length} filled={!!connections.length} />
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
    </Container>
  );
};
