import React, { type FC } from 'react';
import { Role, User } from '@/types';
import { useUserStore } from '@/store';
import styled from 'styled-components';
import { ROLE, ROLE_OPTIONS } from '@/utils';
import { FlexColumn, FlexRow } from '@odigos/ui-kit/components';
import { DataCard, DropDown, Typography } from '@odigos/ui-kit/components/v2';

interface FormData {
  role: Role;
}

interface UserCardProps {
  user: User;
  formData: FormData;
  handleFormChange: (key: keyof FormData, value: Role) => void;
}

const DataCardContentRow = styled(FlexRow)`
  width: 100%;
  gap: 36px;
  align-items: flex-start;
`;

const DataCardContentCell = styled(FlexColumn)`
  line-height: 30px;
`;

const DataCardCellTitle = styled(Typography)`
  color: ${({ theme }) => theme.v2.colors.silver['400']};
  font-size: 12px;
`;

const DataCardCellStretch = styled.div`
  width: 300px;
`;

const UserCard: FC<UserCardProps> = ({ user: _, formData, handleFormChange }) => {
  const { isAdmin } = useUserStore();

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
