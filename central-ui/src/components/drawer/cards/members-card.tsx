import React, { useState, type FC, useMemo } from 'react';
import type { User } from '@/types';
import { useUsersCRUD } from '@/hooks';
import Theme from '@odigos/ui-kit/theme';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { MinWidthWide, Relative } from '@/components/styled';
import { DataCardContentColumn, DataCardContentRow } from './styled';
import { MinusCircledIcon, PlusIcon, SearchIcon, UserGroupIcon } from '@odigos/ui-kit/icons';
import { DataCard, DataFinger, Dropdown, IconButton, Input, NoDataFound, PopupForm } from '@odigos/ui-kit/components';

interface MembersCardProps {
  formData: {
    users?: User[];
  };
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: string, value: any) => void;
}

const MembersCard: FC<MembersCardProps> = ({ formData, handleFormChange }) => {
  const theme = Theme.useTheme();
  const { users } = useUsersCRUD();

  const [isAdd, setIsAdd] = useState(false);
  const toggleAdd = () => setIsAdd((prev) => !prev);
  const { formData: tempFormData, handleFormChange: handleTempFormChange, resetFormData: resetTempFormData } = useGenericForm(formData);

  const [search, setSearch] = useState('');
  const filteredUsers = useMemo(() => formData.users?.filter(({ email }) => email.toLowerCase().includes(search.toLowerCase())), [formData.users, search]);

  const usersCount = formData.users?.length || 0;
  const filteredUsersCount = filteredUsers?.length || 0;

  return (
    <DataCard
      title='Members'
      titleBadge={usersCount}
      action={
        <>
          <Input icon={SearchIcon} placeholder='Search...' value={search} onChange={(e) => setSearch(e.target.value)} />

          <Relative>
            <IconButton size={32} onClick={toggleAdd} withBorder>
              <PlusIcon />
            </IconButton>

            <PopupForm
              flipX
              clientX={36}
              clientY={0}
              isOpen={isAdd}
              title='Add members from the list'
              onClose={() => {
                resetTempFormData();
                toggleAdd();
              }}
              onSave={() => {
                handleFormChange('users', tempFormData.users);
                toggleAdd();
              }}
            >
              <MinWidthWide>
                <Dropdown
                  showSearch
                  isMulti
                  options={users.map((x) => ({ id: x.id, value: x.email }))}
                  value={tempFormData.users?.map((x) => ({ id: x.id, value: x.email }))}
                  onSelect={({ id }) => {
                    const fullUser = users.find((user) => user.id === id);
                    if (fullUser) {
                      handleTempFormChange('users', [...(tempFormData.users || []), fullUser]);
                    }
                  }}
                  onDeselect={({ id }) => {
                    handleTempFormChange(
                      'users',
                      tempFormData.users?.filter((x) => x.id !== id),
                    );
                  }}
                />
              </MinWidthWide>
            </PopupForm>
          </Relative>
        </>
      }
    >
      {!filteredUsersCount ? (
        <DataCardContentRow $justifyContent='center'>
          <NoDataFound subTitle={search ? undefined : ''} />
        </DataCardContentRow>
      ) : (
        <DataCardContentColumn>
          {filteredUsers?.map((x) => (
            <DataFinger
              key={`user-${x.id}`}
              title={x.email}
              subTitle={x.role}
              icons={[UserGroupIcon]}
              renderActions={() => (
                <IconButton
                  tooltip='Remove member'
                  onClick={() => {
                    handleFormChange(
                      'users',
                      formData.users?.filter(({ id }) => id !== x.id),
                    );
                  }}
                >
                  <MinusCircledIcon fill={theme.text.error} />
                </IconButton>
              )}
            />
          ))}
        </DataCardContentColumn>
      )}
    </DataCard>
  );
};

export { MembersCard, type MembersCardProps };
