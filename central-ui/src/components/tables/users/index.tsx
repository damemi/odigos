import React, { useCallback, useMemo, useRef, useState } from 'react';
import { Role } from '@/types';
import * as UTILS from '@/utils';
import { IconCircle } from '../styled';
import { useUsersCRUD } from '@/hooks';
import { useUserStore } from '@/store';
import { useTheme } from 'styled-components';
import { EditUserDrawer, ResetPasswordModal } from '@/components';
import type { User, UserInput } from '@/types';
import type { SVG } from '@odigos/ui-kit/types';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { EditIcon, TrashIcon, UserIcon } from '@odigos/ui-kit/icons';
import { Tag, Table, TableVariant, Typography, TypographySize, WarningModal, type TableProps, type TableRow } from '@odigos/ui-kit/components';

export enum UserTableColumn {
  Icon = 'icon',
  User = 'user',
  Role = 'role',
  // Teams = 'teams',
}

export const UsersTable: React.FC = () => {
  const theme = useTheme();
  const { isAdmin, user: currentUser } = useUserStore();
  const { addNotification } = useNotificationStore();
  const { users, updateUser, updating, deleteUser, deleting, resetUserPassword, loading } = useUsersCRUD();

  const [editOpenForUser, setEditOpenForUser] = useState<User | null>(null);
  const [deleteOpenForUser, setDeleteOpenForUser] = useState<User | null>(null);
  const [resetOpenForUser, setResetOpenForUser] = useState<User | null>(null);
  const [resetPasswordResult, setResetPasswordResult] = useState<{ email: string; password: string } | null>(null);
  // A second reset while one is in flight would leave the modal showing a password that no longer works.
  const resettingPasswordRef = useRef(false);

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

  const handleResetPassword = useCallback(
    async (user: User) => {
      if (resettingPasswordRef.current) return;
      resettingPasswordRef.current = true;
      const result = await resetUserPassword(user.id).finally(() => {
        resettingPasswordRef.current = false;
      });

      if (result?.success && result.generatedPassword) {
        setResetPasswordResult({ email: user.email, password: result.generatedPassword });
      } else if (result) {
        addNotification({ type: StatusType.Error, title: UTILS.RESET_PASSWORD_ERROR, description: result.error || UTILS.RESET_PASSWORD_ERROR });
      }
    },
    [resetUserPassword, addNotification],
  );

  const columns: TableProps['columns'] = useMemo(
    () => [
      { key: UserTableColumn.Icon, label: '', noSort: true, noFilter: true },
      { key: UserTableColumn.User, label: UTILS.EMAIL, noSort: true, noFilter: true },
      { key: UserTableColumn.Role, label: UTILS.ROLE, noSort: true, noFilter: true },
      // { key: UserTableColumn.Teams, label: UTILS.TEAMS },
    ],
    [],
  );

  const rows: TableProps['rows'] = useMemo(
    () =>
      users.map((user) => {
        const UserIconCell = () => (
          <IconCircle>
            <UserIcon strokeWidth={2} size={20} />
          </IconCircle>
        );
        const EmailCell = () => (
          <Typography size={TypographySize.XS} nowrap>
            {user.email}
          </Typography>
        );
        const RoleCell = () => <Tag variant={UTILS.getTagVariantForRole(user.role)} label={UTILS.ROLE_LABELS[user.role] ?? user.role} />;

        return {
          onClick: isAdmin ? () => setEditOpenForUser(user) : undefined,
          cells: [
            { key: UserTableColumn.Icon, rawValue: '', component: UserIconCell },
            { key: UserTableColumn.User, rawValue: user.email, component: EmailCell },
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

      const actions = [
        {
          id: `edit-${user.id}`,
          tooltip: UTILS.EDIT_USER,
          rightIcon: EditIcon,
          onClick: () => setEditOpenForUser(user),
        },
      ];

      const canDelete = user.id !== currentUser?.id && (user.role !== Role.OWNER || currentUser?.role === Role.OWNER);
      if (canDelete) {
        actions.push({
          id: `delete-${user.id}`,
          tooltip: UTILS.DELETE_USER,
          rightIcon: TrashFilled,
          onClick: () => setDeleteOpenForUser(user),
        });
      }

      return actions;
    },
    [isAdmin, users, currentUser?.id, currentUser?.role, theme.text.error],
  );

  return (
    <>
      <Table variant={TableVariant.Pretty} columns={columns} rows={rows} isLoading={loading} noDataProps={{ title: 'No data found' }} getRowActions={isAdmin ? getRowActions : undefined} />

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
            setResetOpenForUser(editOpenForUser);
            setEditOpenForUser(null);
          }}
        />
      )}

      <WarningModal
        isOpen={resetOpenForUser !== null}
        title={UTILS.RESET_PASSWORD}
        description={UTILS.RESET_PASSWORD_CONFIRM((resetOpenForUser?.username || resetOpenForUser?.email || resetOpenForUser?.id) ?? '')}
        onClose={() => setResetOpenForUser(null)}
        approveButton={{
          label: UTILS.RESET_PASSWORD,
          onClick: () => {
            if (resetOpenForUser) handleResetPassword(resetOpenForUser);
            setResetOpenForUser(null);
          },
        }}
        denyButton={{
          label: UTILS.CANCEL,
          onClick: () => setResetOpenForUser(null),
        }}
      />

      {resetPasswordResult && <ResetPasswordModal email={resetPasswordResult.email} password={resetPasswordResult.password} onClose={() => setResetPasswordResult(null)} />}

      <WarningModal
        isOpen={deleteOpenForUser !== null}
        title={UTILS.DELETE_USER}
        description={UTILS.DELETE_USER_CONFIRM((deleteOpenForUser?.username || deleteOpenForUser?.email || deleteOpenForUser?.id) ?? '')}
        onClose={() => setDeleteOpenForUser(null)}
        approveButton={{
          label: UTILS.DELETE,
          onClick: () => {
            handleDeleteUser(deleteOpenForUser?.id as string);
            setDeleteOpenForUser(null);
            setEditOpenForUser(null);
          },
        }}
        denyButton={{
          label: UTILS.CANCEL,
          onClick: () => setDeleteOpenForUser(null),
        }}
      />
    </>
  );
};
