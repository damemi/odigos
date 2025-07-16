import React, { type FC } from 'react';
import type { User } from '@/types';
import { DataCard, Dropdown, Text } from '@odigos/ui-kit/components';
import { DataCardCellStretch, DataCardCellTitle, DataCardContent, DataCardContentCell } from './styled';
import { COMPUTE_PLATFORMS_TAB, CREATE_USER_ROLE_LABEL, PLATFORMS_COUNT, ROLE_COLUMN, ROLE_OPTIONS, TEAMS_COUNT, TEAMS_TAB } from '@/utils';

interface UserCardProps {
  formData: Pick<User, 'role' | 'teams' | 'computePlatforms'>;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: string, value: any) => void;
}

const UserCard: FC<UserCardProps> = ({ formData, handleFormChange }) => {
  const teamsCount = formData.teams?.length || 0;
  const platformsCount = formData.computePlatforms?.length || 0;

  return (
    <DataCard title=''>
      <DataCardContent>
        <DataCardContentCell>
          <DataCardCellTitle>{ROLE_COLUMN}</DataCardCellTitle>
          <DataCardCellStretch>
            <Dropdown
              value={ROLE_OPTIONS.find(({ id }) => id === formData.role)}
              onSelect={(opt) => handleFormChange('role', opt.id)}
              onDeselect={() => {}}
              options={ROLE_OPTIONS}
              placeholder={CREATE_USER_ROLE_LABEL}
              required
            />
          </DataCardCellStretch>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{TEAMS_TAB}</DataCardCellTitle>
          <Text>{TEAMS_COUNT(teamsCount)}</Text>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{COMPUTE_PLATFORMS_TAB}</DataCardCellTitle>
          <Text>{PLATFORMS_COUNT(platformsCount)}</Text>
        </DataCardContentCell>
      </DataCardContent>
    </DataCard>
  );
};

export { UserCard, type UserCardProps };
