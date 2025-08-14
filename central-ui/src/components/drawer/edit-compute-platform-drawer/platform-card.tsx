import React, { type FC } from 'react';
import type { ComputePlatform } from '@/types';
import { DataCard, Text } from '@odigos/ui-kit/components';
import { MEMBERS_COUNT, TEAMS_TAB, TYPE, USERS_COUNT, USERS_TAB } from '@/utils';
import { DataCardCellTitle, DataCardContentRow, DataCardContentCell } from '../cards/styled';

interface PlatformCardProps {
  computePlatform: ComputePlatform;
}

const PlatformCard: FC<PlatformCardProps> = ({ computePlatform }) => {
  const usersCount = computePlatform.users?.length || 0;
  const teamsCount = computePlatform.teams?.length || 0;

  return (
    <DataCard title=''>
      <DataCardContentRow>
        <DataCardContentCell>
          <DataCardCellTitle>{TYPE}</DataCardCellTitle>
          <Text>{computePlatform.type}</Text>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{USERS_TAB}</DataCardCellTitle>
          <Text>{USERS_COUNT(usersCount)}</Text>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{TEAMS_TAB}</DataCardCellTitle>
          <Text>{MEMBERS_COUNT(teamsCount)}</Text>
        </DataCardContentCell>
      </DataCardContentRow>
    </DataCard>
  );
};

export { PlatformCard, type PlatformCardProps };
