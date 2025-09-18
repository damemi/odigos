import React, { useState, type FC, useMemo } from 'react';
import type { User } from '@/types';
import { useUsersCRUD } from '@/hooks';
import { useUserStore } from '@/store';
import Theme from '@odigos/ui-kit/theme';
import { MinWidthWide, Relative } from '@/components/styled';
import { DataCardContentColumn, DataCardContentRow } from './styled';
import { MinusCircledIcon, PlusIcon, SearchIcon, UserGroupIcon } from '@odigos/ui-kit/icons';
import { DataCard, DataFinger, Dropdown, IconButton, Input, NoDataFound, PopupForm } from '@odigos/ui-kit/components';

interface FormData {
  addUserIds: string[];
  removeUserIds: string[];
}

interface MembersCardProps {
  currentUsers: User[];
  formData: FormData;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: keyof FormData, value: any) => void;
}

const MembersCard: FC<MembersCardProps> = ({ currentUsers, formData, handleFormChange }) => {
  const theme = Theme.useTheme();
  const { users } = useUsersCRUD();
  const { isAdmin } = useUserStore();

  const [search, setSearch] = useState('');
  const [isAdd, setIsAdd] = useState(false);
  const toggleAdd = () => setIsAdd((prev) => !prev);

  const filteredUsers = useMemo(() => {
    const arr =
      currentUsers.filter(({ id, email }) => {
        return !formData.removeUserIds.includes(id) && email.toLowerCase().includes(search.toLowerCase());
      }) || [];

    formData.addUserIds.forEach((id) => {
      const user = users.find((user) => user.id === id);
      const exists = arr.find((user) => user.id === id);
      if (user && !exists) {
        arr.push(user);
      }
    });

    return arr;
  }, [currentUsers, search, users, formData]);

  return (
    <DataCard
      title='Members'
      titleBadge={currentUsers.length}
      action={
        <>
          <Input icon={SearchIcon} placeholder='Search...' value={search} onChange={(e) => setSearch(e.target.value)} />

          <Relative>
            {isAdmin && (
              <IconButton size={32} onClick={toggleAdd} withBorder>
                <PlusIcon />
              </IconButton>
            )}

            <PopupForm
              flipX
              clientX={36}
              clientY={0}
              isOpen={isAdd}
              title='Add members from the list'
              onClose={() => {
                handleFormChange('addUserIds', []);
                toggleAdd();
              }}
              onSave={() => {
                toggleAdd();
              }}
            >
              <MinWidthWide>
                <Dropdown
                  showSearch
                  isMulti
                  options={users.map((user) => ({ id: user.id, value: user.email }))}
                  value={formData.addUserIds.map((id) => {
                    const user = users.find((user) => user.id === id);
                    return { id, value: user?.email || user?.username || id };
                  })}
                  onSelect={({ id }) => {
                    if (id) {
                      if (formData.removeUserIds.includes(id)) {
                        const newRemoveUserIds = formData.removeUserIds.filter((str) => str !== id);
                        handleFormChange('removeUserIds', newRemoveUserIds);
                      }

                      const foundAdd = formData.addUserIds.find((str) => str === id);
                      const foundCurrent = currentUsers.find((user) => user.id === id);
                      if (!foundAdd && !foundCurrent) {
                        const newAddUserIds = [...formData.addUserIds, id];
                        handleFormChange('addUserIds', newAddUserIds);
                      }
                    }
                  }}
                  onDeselect={({ id }) => {
                    if (id) {
                      if (formData.addUserIds.includes(id)) {
                        const newAddUserIds = formData.addUserIds.filter((str) => str !== id);
                        handleFormChange('addUserIds', newAddUserIds);
                      }
                      // note: do not handle the 'remove' list here, it's not needed
                    }
                  }}
                />
              </MinWidthWide>
            </PopupForm>
          </Relative>
        </>
      }
    >
      {!filteredUsers.length ? (
        <DataCardContentRow $justifyContent='center'>
          <NoDataFound subTitle={search ? undefined : ''} />
        </DataCardContentRow>
      ) : (
        <DataCardContentColumn>
          {filteredUsers?.map(({ id, email, username, role }) => (
            <DataFinger
              key={`user-${id}`}
              title={email || username}
              subTitle={role}
              icons={[UserGroupIcon]}
              renderActions={
                isAdmin
                  ? () => (
                      <IconButton
                        tooltip='Remove member'
                        onClick={() => {
                          if (formData.addUserIds.includes(id)) {
                            const newAddUserIds = formData.addUserIds.filter((str) => str !== id);
                            handleFormChange('addUserIds', newAddUserIds);
                          }

                          const foundRemove = formData.removeUserIds.find((str) => str === id);
                          const foundCurrent = currentUsers.find((user) => user.id === id);
                          if (!foundRemove && foundCurrent) {
                            const newRemoveUserIds = [...formData.removeUserIds, id];
                            handleFormChange('removeUserIds', newRemoveUserIds);
                          }
                        }}
                      >
                        <MinusCircledIcon fill={theme.text.error} />
                      </IconButton>
                    )
                  : undefined
              }
            />
          ))}
        </DataCardContentColumn>
      )}
    </DataCard>
  );
};

export { MembersCard, type MembersCardProps };
