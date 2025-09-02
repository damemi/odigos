import React, { type FC } from 'react';
import type { ComputePlatform } from '@/types';
import { DataCard, Text } from '@odigos/ui-kit/components';
import { MEMBERS_COUNT, TEAMS, TYPE } from '@/utils';
import { DataCardCellTitle, DataCardContentRow, DataCardContentCell } from '../cards/styled';

interface PlatformCardProps {
  computePlatform: ComputePlatform;
}

const PlatformCard: FC<PlatformCardProps> = ({ computePlatform }) => {
  // TODO: uncomment when user attributes are implemented
  // const usersCount = computePlatform.users?.length || 0;
  const teamsCount = computePlatform.teams?.length || 0;

  return (
    <DataCard title=''>
      <DataCardContentRow>
        <DataCardContentCell>
          <DataCardCellTitle>{TYPE}</DataCardCellTitle>
          <Text>{computePlatform.type}</Text>
        </DataCardContentCell>
        {/* TODO: uncomment when user attributes are implemented */}
        {/* <DataCardContentCell>
          <DataCardCellTitle>{USERS}</DataCardCellTitle>
          <Text>{USERS_COUNT(usersCount)}</Text>
        </DataCardContentCell> */}
        <DataCardContentCell>
          <DataCardCellTitle>{TEAMS}</DataCardCellTitle>
          <Text>{MEMBERS_COUNT(teamsCount)}</Text>
        </DataCardContentCell>
      </DataCardContentRow>
    </DataCard>
  );
};

export { PlatformCard, type PlatformCardProps };
