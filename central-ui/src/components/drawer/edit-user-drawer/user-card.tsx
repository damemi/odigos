import React, { type FC } from 'react';
import type { Role, User } from '@/types';
import { DataCard, Dropdown, Text } from '@odigos/ui-kit/components';
import { DataCardCellStretch, DataCardCellTitle, DataCardContentRow, DataCardContentCell } from '../cards/styled';
import { CREATE_USER_ROLE_LABEL, ROLE_COLUMN, ROLE_OPTIONS, TEAMS_COUNT, TEAMS_TAB } from '@/utils';

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
          <DataCardCellTitle>{ROLE_COLUMN}</DataCardCellTitle>
          <DataCardCellStretch>
            <Dropdown
              placeholder={CREATE_USER_ROLE_LABEL}
              options={ROLE_OPTIONS}
              value={ROLE_OPTIONS.find(({ id }) => id === formData.role)}
              onSelect={(opt) => handleFormChange('role', opt.id)}
              onDeselect={() => {}}
              required
            />
          </DataCardCellStretch>
        </DataCardContentCell>
        <DataCardContentCell>
          <DataCardCellTitle>{TEAMS_TAB}</DataCardCellTitle>
          <Text>{TEAMS_COUNT(teamsCount)}</Text>
        </DataCardContentCell>
        {/* TODO: uncomment when user attributes are implemented */}
        {/* <DataCardContentCell>
          <DataCardCellTitle>{COMPUTE_PLATFORMS_TAB}</DataCardCellTitle>
          <Text>{PLATFORMS_COUNT(platformsCount)}</Text>
        </DataCardContentCell> */}
      </DataCardContentRow>
    </DataCard>
  );
};

export { UserCard, type UserCardProps };
