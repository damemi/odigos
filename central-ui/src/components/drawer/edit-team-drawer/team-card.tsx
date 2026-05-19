import React, { type FC } from 'react';
import type { Team } from '@/types';
import { useUserStore } from '@/store';
import { DataCard, TextArea, Typography } from '@odigos/ui-kit/components/v2';
import { COMPUTE_PLATFORMS, DESCRIPTION, MEMBERS, MEMBERS_COUNT, PLATFORMS_COUNT } from '@/utils';
import { DataCardCellStretch, DataCardCellTitle, DataCardContentCell, DataCardContentRow } from '../cards/styled';

interface FormData {
  description: string;
}

interface TeamCardProps {
  team: Team;
  formData: FormData;
  handleFormChange: (key: keyof FormData, value: string) => void;
}

const TeamCard: FC<TeamCardProps> = ({ team, formData, handleFormChange }) => {
  const { isAdmin } = useUserStore();

  const usersCount = team.users?.length || 0;
  const platformsCount = team.computePlatforms?.length || 0;

  return (
    <DataCard>
      <DataCardContentRow>
        <DataCardContentCell>
          <DataCardCellTitle>{DESCRIPTION}</DataCardCellTitle>
          <DataCardCellStretch>
            <TextArea value={formData.description || ''} onChange={(e) => handleFormChange('description', e.target.value)} required disabled={!isAdmin} />
          </DataCardCellStretch>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{MEMBERS}</DataCardCellTitle>
          <Typography>{MEMBERS_COUNT(usersCount)}</Typography>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{COMPUTE_PLATFORMS}</DataCardCellTitle>
          <Typography>{PLATFORMS_COUNT(platformsCount)}</Typography>
        </DataCardContentCell>
      </DataCardContentRow>
    </DataCard>
  );
};

export { TeamCard, type TeamCardProps };
