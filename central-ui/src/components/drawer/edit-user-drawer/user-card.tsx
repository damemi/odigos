import React, { type FC } from 'react';
import { Role, User } from '@/types';
import { useUserStore } from '@/store';
import { ROLE, ROLE_OPTIONS } from '@/utils';
import { DataCard, DropDown } from '@odigos/ui-kit/components/v2';
import { DataCardCellStretch, DataCardCellTitle, DataCardContentCell, DataCardContentRow } from '../cards/styled';

interface FormData {
  role: Role;
}

interface UserCardProps {
  user: User;
  formData: FormData;
  handleFormChange: (key: keyof FormData, value: Role) => void;
}

const UserCard: FC<UserCardProps> = ({ user: _, formData, handleFormChange }) => {
  const { isAdmin } = useUserStore();

  // const teamsCount = user.teams?.length || 0;
  // TODO: uncomment when user attributes are implemented
  // const platformsCount = user.computePlatforms?.length || 0;

  return (
    <DataCard>
      <DataCardContentRow>
        <DataCardContentCell>
          <DataCardCellTitle>{ROLE}</DataCardCellTitle>
          <DataCardCellStretch>
            <DropDown
              placeholder={ROLE}
              options={ROLE_OPTIONS}
              disabled={!isAdmin}
              values={[formData.role]}
              setValues={([value]) => handleFormChange('role', (value as Role) ?? Role.VIEWER)}
              required
            />
          </DataCardCellStretch>
        </DataCardContentCell>
        {/* <DataCardContentCell>
          <DataCardCellTitle>{TEAMS}</DataCardCellTitle>
          <Typography>{TEAMS_COUNT(teamsCount)}</Typography>
        </DataCardContentCell> */}
        {/* TODO: uncomment when user attributes are implemented */}
        {/* <DataCardContentCell>
          <DataCardCellTitle>{COMPUTE_PLATFORMS}</DataCardCellTitle>
          <Typography>{PLATFORMS_COUNT(platformsCount)}</Typography>
        </DataCardContentCell> */}
      </DataCardContentRow>
    </DataCard>
  );
};

export { UserCard, type UserCardProps };
