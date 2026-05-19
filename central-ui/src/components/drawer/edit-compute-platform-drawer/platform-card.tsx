import React, { type FC } from 'react';
import type { ComputePlatform } from '@/types';
import { MEMBERS_COUNT, TEAMS, TYPE } from '@/utils';
import { DataCard, Typography } from '@odigos/ui-kit/components/v2';
import { DataCardCellTitle, DataCardContentCell, DataCardContentRow } from '../cards/styled';

interface PlatformCardProps {
  computePlatform: ComputePlatform;
}

const PlatformCard: FC<PlatformCardProps> = ({ computePlatform }) => {
  // TODO: uncomment when user attributes are implemented
  // const usersCount = computePlatform.users?.length || 0;
  const teamsCount = computePlatform.teams?.length || 0;

  return (
    <DataCard>
      <DataCardContentRow>
        <DataCardContentCell>
          <DataCardCellTitle>{TYPE}</DataCardCellTitle>
          <Typography>{computePlatform.type}</Typography>
        </DataCardContentCell>
        {/* TODO: uncomment when user attributes are implemented */}
        {/* <DataCardContentCell>
          <DataCardCellTitle>{USERS}</DataCardCellTitle>
          <Typography>{USERS_COUNT(usersCount)}</Typography>
        </DataCardContentCell> */}
        <DataCardContentCell>
          <DataCardCellTitle>{TEAMS}</DataCardCellTitle>
          <Typography>{MEMBERS_COUNT(teamsCount)}</Typography>
        </DataCardContentCell>
      </DataCardContentRow>
    </DataCard>
  );
};

export { PlatformCard, type PlatformCardProps };
