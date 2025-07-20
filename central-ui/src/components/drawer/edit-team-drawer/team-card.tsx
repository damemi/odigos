import React, { type FC } from 'react';
import type { Team } from '@/types';
import { DataCard, Text, TextArea } from '@odigos/ui-kit/components';
import { DataCardCellStretch, DataCardCellTitle, DataCardContentRow, DataCardContentCell } from '../cards/styled';
import { COMPUTE_PLATFORMS_TAB, DESCRIPTION, MEMBERS_COUNT, PLATFORMS_COUNT, TEAMS_TAB } from '@/utils';

interface TeamCardProps {
  formData: Pick<Team, 'description' | 'users' | 'computePlatforms'>;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: string, value: any) => void;
}

const TeamCard: FC<TeamCardProps> = ({ formData, handleFormChange }) => {
  const usersCount = formData.users?.length || 0;
  const platformsCount = formData.computePlatforms?.length || 0;

  return (
    <DataCard title=''>
      <DataCardContentRow>
        <DataCardContentCell>
          <DataCardCellTitle>{TEAMS_TAB}</DataCardCellTitle>
          <Text>{MEMBERS_COUNT(usersCount)}</Text>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{COMPUTE_PLATFORMS_TAB}</DataCardCellTitle>
          <Text>{PLATFORMS_COUNT(platformsCount)}</Text>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{DESCRIPTION}</DataCardCellTitle>
          <DataCardCellStretch>
            <TextArea value={formData.description} onChange={(e) => handleFormChange('description', e.target.value)} required />
          </DataCardCellStretch>
        </DataCardContentCell>
      </DataCardContentRow>
    </DataCard>
  );
};

export { TeamCard, type TeamCardProps };
