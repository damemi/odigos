import React, { type FC } from 'react';
import { Role, User } from '@/types';
import { ROLE, ROLE_OPTIONS, TEAMS_COUNT, TEAMS } from '@/utils';
import { DataCard, Dropdown, Text } from '@odigos/ui-kit/components';
import { DataCardCellStretch, DataCardCellTitle, DataCardContentRow, DataCardContentCell } from '../cards/styled';

interface FormData {
  role: Role;
}

interface UserCardProps {
  user: User;
  formData: FormData;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: keyof FormData, value: any) => void;
}

const UserCard: FC<UserCardProps> = ({ user, formData, handleFormChange }) => {
  const teamsCount = user.teams?.length || 0;
  // TODO: uncomment when user attributes are implemented
  // const platformsCount = user.computePlatforms?.length || 0;

  return (
    <DataCard title=''>
      <DataCardContentRow>
        <DataCardContentCell>
          <DataCardCellTitle>{ROLE}</DataCardCellTitle>
          <DataCardCellStretch>
            <Dropdown
              placeholder={ROLE}
              options={ROLE_OPTIONS}
              disabled={user.role !== Role.ADMIN}
              value={ROLE_OPTIONS.find(({ id }) => id === formData.role)}
              onSelect={(opt) => handleFormChange('role', opt.id)}
              onDeselect={() => {}}
              required
            />
          </DataCardCellStretch>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{TEAMS}</DataCardCellTitle>
          <Text>{TEAMS_COUNT(teamsCount)}</Text>
        </DataCardContentCell>
        {/* TODO: uncomment when user attributes are implemented */}
        {/* <DataCardContentCell>
          <DataCardCellTitle>{COMPUTE_PLATFORMS}</DataCardCellTitle>
          <Text>{PLATFORMS_COUNT(platformsCount)}</Text>
        </DataCardContentCell> */}
      </DataCardContentRow>
    </DataCard>
  );
};

export { UserCard, type UserCardProps };
