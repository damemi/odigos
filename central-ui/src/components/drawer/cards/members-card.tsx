import React, { useState, type FC, useMemo } from 'react';
import type { User } from '@/types';
import Theme from '@odigos/ui-kit/theme';
import { DataCardContent } from './styled';
import { MinusCircledIcon, SearchIcon, UserGroupIcon } from '@odigos/ui-kit/icons';
import { DataCard, DataFinger, IconButton, Input, NoDataFound } from '@odigos/ui-kit/components';

interface MembersCardProps {
  formData: {
    users?: User[];
  };
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: string, value: any) => void;
}

const MembersCard: FC<MembersCardProps> = ({ formData, handleFormChange }) => {
  const theme = Theme.useTheme();

  const [search, setSearch] = useState('');
  const filteredUsers = useMemo(() => formData.users?.filter(({ email }) => email.toLowerCase().includes(search.toLowerCase())), [formData.users, search]);

  const usersCount = formData.users?.length || 0;
  const filteredUsersCount = filteredUsers?.length || 0;

  return (
    <DataCard title='Members' titleBadge={usersCount} action={<Input icon={SearchIcon} placeholder='Search...' value={search} onChange={(e) => setSearch(e.target.value)} />}>
      <DataCardContent $justifyContent={!filteredUsersCount ? 'center' : ''}>
        {!filteredUsersCount ? (
          <NoDataFound subTitle={search ? undefined : ''} />
        ) : (
          filteredUsers?.map((u) => (
            <DataFinger
              key={`user-${u.id}`}
              title={u.email}
              subTitle={u.role}
              icons={[UserGroupIcon]}
              renderActions={() => (
                <IconButton
                  onClick={() =>
                    handleFormChange(
                      'users',
                      formData.users?.filter(({ id }) => id !== u.id),
                    )
                  }
                  tooltip='Remove member'
                >
                  <MinusCircledIcon fill={theme.text.error} />
                </IconButton>
              )}
            />
          ))
        )}
      </DataCardContent>
    </DataCard>
  );
};

export { MembersCard, type MembersCardProps };
