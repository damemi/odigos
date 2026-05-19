import React, { useMemo, useState, type FC } from 'react';
import type { User } from '@/types';
import { useUsersCRUD } from '@/hooks';
import { useUserStore } from '@/store';
import { useTheme } from 'styled-components';
import type { SVG } from '@odigos/ui-kit/types';
import { MinWidthWide, Relative } from '@/components/styled';
import { PopupForm, Tooltip } from '@odigos/ui-kit/components';
import { MinusCircledIcon, PlusIcon, UserGroupIcon } from '@odigos/ui-kit/icons';
import { Badge, DropDown, IconButton, IconButtonSize, NoData, Search, Typography, TypographyColor, TypographySize } from '@odigos/ui-kit/components/v2';
import { DataCardContentColumn, DataCardContentRow, ListCard, ListCardHeader, ListCardHeaderLeft, ListCardHeaderRight, ListRow, ListRowMain, ListRowText } from './styled';

interface FormData {
  addUserIds: string[];
  removeUserIds: string[];
}

interface MembersCardProps {
  currentUsers: User[];
  formData: FormData;
  handleFormChange: (key: keyof FormData, value: string[]) => void;
}

const MembersCard: FC<MembersCardProps> = ({ currentUsers, formData, handleFormChange }) => {
  const theme = useTheme();
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

  const RemoveIcon = useMemo<SVG>(() => {
    const Cmp: SVG = (props) => <MinusCircledIcon fill={theme.text.error} {...props} />;
    return Cmp;
  }, [theme]);

  return (
    <ListCard>
      <ListCardHeader>
        <ListCardHeaderLeft>
          <Typography>Members</Typography>
          <Badge label={currentUsers.length} />
        </ListCardHeaderLeft>
        <ListCardHeaderRight>
          <Search value={search} onChange={setSearch} placeholder='Search...' />
          <Relative>
            {isAdmin && <IconButton icon={PlusIcon} size={IconButtonSize.M} onClick={toggleAdd} />}

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
                <DropDown
                  withSearch
                  withMultiSelect
                  options={users.map((user) => ({ id: user.id, label: user.email }))}
                  values={formData.addUserIds}
                  setValues={(ids) => {
                    const filtered = ids.filter((id) => !currentUsers.find((u) => u.id === id));
                    handleFormChange('addUserIds', filtered);

                    const removed = formData.removeUserIds.filter((id) => !ids.includes(id));
                    if (removed.length !== formData.removeUserIds.length) {
                      handleFormChange('removeUserIds', removed);
                    }
                  }}
                />
              </MinWidthWide>
            </PopupForm>
          </Relative>
        </ListCardHeaderRight>
      </ListCardHeader>

      {!filteredUsers.length ? (
        <DataCardContentRow $justifyContent='center'>
          <NoData subTitle={search ? undefined : ''} />
        </DataCardContentRow>
      ) : (
        <DataCardContentColumn>
          {filteredUsers.map(({ id, email, username, role }) => (
            <ListRow key={`user-${id}`}>
              <ListRowMain>
                <UserGroupIcon size={16} />
                <ListRowText>
                  <Typography size={TypographySize.S}>{email || username}</Typography>
                  <Typography size={TypographySize.XS} color={TypographyColor.Secondary}>
                    {role}
                  </Typography>
                </ListRowText>
              </ListRowMain>
              {isAdmin && (
                <Tooltip text='Remove member'>
                  <IconButton
                    icon={RemoveIcon}
                    size={IconButtonSize.S}
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
                  />
                </Tooltip>
              )}
            </ListRow>
          ))}
        </DataCardContentColumn>
      )}
    </ListCard>
  );
};

export { MembersCard, type MembersCardProps };
