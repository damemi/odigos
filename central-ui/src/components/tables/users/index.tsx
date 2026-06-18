import React, { useCallback, useMemo, useState } from 'react';
import * as UTILS from '@/utils';
import { IconCircle } from '../styled';
import { useUsersCRUD } from '@/hooks';
import { useUserStore } from '@/store';
import { useTheme } from 'styled-components';
import { EditUserDrawer } from '@/components';
import type { User, UserInput } from '@/types';
import type { SVG } from '@odigos/ui-kit/types';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { EditIcon, TrashIcon, UserIcon } from '@odigos/ui-kit/icons';
import { Badge, Table, TableVariant, WarningModal, type TableProps, type TableRow } from '@odigos/ui-kit/components/v2';

export enum UserTableColumn {
  Icon = 'icon',
  User = 'user',
  Role = 'role',
  // Teams = 'teams',
}

export const UsersTable: React.FC = () => {
  const theme = useTheme();
  const { isAdmin } = useUserStore();
  const { addNotification } = useNotificationStore();
  const { users, updateUser, updating, deleteUser, deleting } = useUsersCRUD();

  const [editOpenForUser, setEditOpenForUser] = useState<User | null>(null);
  const [deleteOpenForUser, setDeleteOpenForUser] = useState<User | null>(null);

  const handleUpdateUser = useCallback(
    async (userID: string, input: UserInput) => {
      try {
        if (updating) return;
        const result = await updateUser(userID, input);

        if (!result?.success) {
          const errorMessage = result?.error || 'Failed to update user';
          addNotification({ type: StatusType.Error, title: Crud.Update, description: errorMessage });
        }
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : 'An unexpected error occurred while updating the user.';
        addNotification({ type: StatusType.Error, title: Crud.Update, description: errorMessage });
      }
    },
    [updating, updateUser, addNotification],
  );

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
    [deleting, deleteUser, addNotification],
  );

  const columns: TableProps['columns'] = useMemo(
    () => [
      { key: UserTableColumn.Icon, label: '', noSort: true, noFilter: true },
      { key: UserTableColumn.User, label: UTILS.USERS },
      { key: UserTableColumn.Role, label: UTILS.ROLE },
      // { key: UserTableColumn.Teams, label: UTILS.TEAMS },
    ],
    [],
  );

  const rows: TableProps['rows'] = useMemo(
    () =>
      users.map((user) => {
        const UserIconCell: React.FC = () => (
          <IconCircle>
            <UserIcon strokeWidth={2} size={20} />
          </IconCircle>
        );
        const RoleCell: React.FC = () => <Badge status={UTILS.getStatusForRole(user.role)} label={user.role} />;

        return {
          onClick: isAdmin ? () => setEditOpenForUser(user) : undefined,
          cells: [
            { key: UserTableColumn.Icon, rawValue: '', component: UserIconCell },
            { key: UserTableColumn.User, rawValue: user.email },
            { key: UserTableColumn.Role, rawValue: user.role, component: RoleCell },
            // { key: UserTableColumn.Teams, rawValue: UTILS.TEAMS_COUNT(user.teams?.length || 0) },
          ],
        };
      }),
    [users, isAdmin],
  );

  const getRowActions: TableProps['getRowActions'] = useCallback(
    ({ cells }: TableRow) => {
      if (!isAdmin) return [];

      const user = users.find((u) => u.email === cells.find((c) => c.key === UserTableColumn.User)?.rawValue);
      if (!user) return [];

      const TrashFilled: SVG = (props) => <TrashIcon fill={theme.text.error} {...props} />;

      return [
        {
          id: `edit-${user.id}`,
          tooltip: 'Edit user',
          rightIcon: EditIcon,
          onClick: () => setEditOpenForUser(user),
        },
        {
          id: `delete-${user.id}`,
          tooltip: 'Delete user',
          rightIcon: TrashFilled,
          onClick: () => setDeleteOpenForUser(user),
        },
      ];
    },
    [isAdmin, users, theme.text.error],
  );

  return (
    <>
      <Table variant={TableVariant.Pretty} columns={columns} rows={rows} noDataProps={{ title: 'No data found' }} getRowActions={isAdmin ? getRowActions : undefined} />

      {editOpenForUser && (
        <EditUserDrawer
          user={editOpenForUser}
          onClose={() => {
            setEditOpenForUser(null);
          }}
          onSave={(payload) => {
            handleUpdateUser(editOpenForUser.id, payload);
            setEditOpenForUser(null);
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

      <WarningModal
        isOpen={deleteOpenForUser !== null}
        title='Delete user'
        description={`Are you sure you want to delete "${(deleteOpenForUser?.username || deleteOpenForUser?.email || deleteOpenForUser?.id) ?? ''}"?`}
        onClose={() => setDeleteOpenForUser(null)}
        approveButton={{
          label: 'Delete',
          onClick: () => {
            handleDeleteUser(deleteOpenForUser?.id as string);
            setDeleteOpenForUser(null);
            setEditOpenForUser(null);
          },
        }}
        denyButton={{
          label: 'Cancel',
          onClick: () => setDeleteOpenForUser(null),
        }}
      />
    </>
  );
};
