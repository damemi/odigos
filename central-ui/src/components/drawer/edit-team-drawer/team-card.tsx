import React, { type FC } from 'react';
import type { Team } from '@/types';
import { DataCard, Text, TextArea } from '@odigos/ui-kit/components';
import { COMPUTE_PLATFORMS_TAB, DESCRIPTION, MEMBERS_COUNT, MEMBERS_LABEL, PLATFORMS_COUNT } from '@/utils';
import { DataCardCellStretch, DataCardCellTitle, DataCardContentRow, DataCardContentCell } from '../cards/styled';

interface FormData {
  description: string;
}

interface TeamCardProps {
  team: Team;
  formData: FormData;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: keyof FormData, value: any) => void;
}

const TeamCard: FC<TeamCardProps> = ({ team, formData, handleFormChange }) => {
  const usersCount = team.users?.length || 0;
  const platformsCount = team.computePlatforms?.length || 0;

  return (
    <DataCard title=''>
      <DataCardContentRow>
        <DataCardContentCell>
          <DataCardCellTitle>{DESCRIPTION}</DataCardCellTitle>
          <DataCardCellStretch>
            <TextArea value={formData.description || ''} onChange={(e) => handleFormChange('description', e.target.value)} required />
          </DataCardCellStretch>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{MEMBERS_LABEL}</DataCardCellTitle>
          <Text>{MEMBERS_COUNT(usersCount)}</Text>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{COMPUTE_PLATFORMS_TAB}</DataCardCellTitle>
          <Text>{PLATFORMS_COUNT(platformsCount)}</Text>
        </DataCardContentCell>
      </DataCardContentRow>
    </DataCard>
  );
};

export { TeamCard, type TeamCardProps };
