import React, { useMemo, useCallback, useState } from 'react';
import * as UTILS from '@/utils';
import type { User } from '@/types';
import { useUsersCRUD } from '@/hooks';
import Theme from '@odigos/ui-kit/theme';
import { EditUserDrawer } from '@/components';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { EditIcon, MinusCircledIcon, UserIcon } from '@odigos/ui-kit/icons';
import { CenterThis, DeleteWarning, FlexRow, IconButton, IconWrapped, InteractiveTable, NoDataFound, RowCell, Tag } from '@odigos/ui-kit/components';

export enum UserTableColumn {
  Icon = 'icon',
  User = 'user',
  Role = 'role',
  Teams = 'teams',
  Platforms = 'platforms',
  Actions = 'actions',
}

export const UsersTable: React.FC = () => {
  const theme = Theme.useTheme();
  const { addNotification } = useNotificationStore();
  const { users, deleteUser, deleting } = useUsersCRUD();

  const [editOpenForUser, setEditOpenForUser] = useState<User | null>(null);
  const [deleteOpenForUser, setDeleteOpenForUser] = useState<User | null>(null);

  const handleDeleteUser = useCallback(
    async (userID: string) => {
      try {
        if (deleting) return;
        const result = await deleteUser(userID);

        if (!result?.success) {
          const errorMessage = result?.error || 'Failed to delete user';
          addNotification({ type: StatusType.Error, title: Crud.Delete, description: errorMessage });
        }
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : 'An unexpected error occurred while deleting the user.';
        addNotification({ type: StatusType.Error, title: Crud.Delete, description: errorMessage });
      }
    },
    [deleting, deleteUser],
  );

  const columns = [
    { key: UserTableColumn.Icon, title: '', sortable: true },
    { key: UserTableColumn.User, title: UTILS.USERS_TAB, sortable: true },
    { key: UserTableColumn.Role, title: UTILS.ROLE_COLUMN, sortable: true },
    { key: UserTableColumn.Teams, title: UTILS.TEAMS_TAB, sortable: true },
    { key: UserTableColumn.Platforms, title: UTILS.COMPUTE_PLATFORMS_TAB, sortable: true },
    { key: UserTableColumn.Actions, title: UTILS.ACTIONS_COLUMN },
  ];

  const rows = useMemo(
    () =>
      users.map((user) => ({
        onClick: () => setEditOpenForUser(user),
        cells: [
          {
            columnKey: UserTableColumn.Icon,
            component: () => <IconWrapped icon={UserIcon} />,
          },
          {
            columnKey: UserTableColumn.User,
            value: user.email,
          },
          {
            columnKey: UserTableColumn.Role,
            component: () => <Tag type={UTILS.getStatusForRole(user.role)}>{user.role}</Tag>,
          },
          {
            columnKey: UserTableColumn.Teams,
            value: UTILS.TEAMS_COUNT(user.teams?.length || 0),
          },
          {
            columnKey: UserTableColumn.Platforms,
            value: UTILS.PLATFORMS_COUNT(user.computePlatforms?.length || 0),
          },
          {
            columnKey: UserTableColumn.Actions,
            component: () => (
              <FlexRow>
                <IconButton onClick={() => setEditOpenForUser(user)} tooltip='Edit user'>
                  <EditIcon />
                </IconButton>
                <IconButton onClick={() => setDeleteOpenForUser(user)} tooltip='Delete user'>
                  <MinusCircledIcon fill={theme.text.error} />
                </IconButton>
              </FlexRow>
            ),
          },
        ] as RowCell[],
      })),
    [users, handleDeleteUser],
  );

  return (
    <>
      <InteractiveTable columns={columns} rows={rows} />
      {!rows.length && (
        <CenterThis style={{ marginTop: '2rem' }}>
          <NoDataFound />
        </CenterThis>
      )}

      {editOpenForUser && (
        <EditUserDrawer
          user={editOpenForUser}
          onClose={() => {
            setEditOpenForUser(null);
          }}
          onSave={() => {
            alert('TODO');
          }}
          onDelete={() => {
            setDeleteOpenForUser(editOpenForUser);
            setEditOpenForUser(null);
          }}
          onResetPassword={() => {
            alert('TODO');
          }}
        />
      )}

      <DeleteWarning
        isOpen={deleteOpenForUser !== null}
        name={deleteOpenForUser?.username || deleteOpenForUser?.email || deleteOpenForUser?.id}
        onApprove={() => {
          handleDeleteUser(deleteOpenForUser?.id as string);
          setDeleteOpenForUser(null);
        }}
        onDeny={() => {
          setDeleteOpenForUser(null);
        }}
      />
    </>
  );
};
