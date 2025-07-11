import React, { useMemo, useCallback } from 'react';
import styled from 'styled-components';
import * as STRINGS from '@/utils';
import type { User } from '@/types';
import { RoleBadge } from '@/components';
import { InteractiveTable, RowCell } from '@odigos/ui-kit/components';
import { MinusIcon, TrashIcon } from '@odigos/ui-kit/icons';
import { useUsersCRUD } from '@/hooks';

interface UsersTableProps {
  users: User[];
}

export enum UserTableColumn {
  Icon = 'icon',
  User = 'user',
  Role = 'role',
  Teams = 'teams',
  Platforms = 'platforms',
  Actions = 'actions',
}

const UserIconWrapper = styled.div`
  border-radius: 8px;
  background: rgba(249, 249, 249, 0.04);
  display: flex;
  width: 36px;
  height: 36px;
  justify-content: center;
  align-items: center;
  gap: 8px;
`;

const ActionButton = styled.button`
  background: none;
  border: none;
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  transition: background-color 0.2s ease;

  &:hover {
    background-color: rgba(255, 255, 255, 0.1);
  }

  &:disabled {
    cursor: not-allowed;
    opacity: 0.5;
  }
`;

export const UsersTable: React.FC<UsersTableProps> = ({ users }) => {
  const { deleteUser, deleting } = useUsersCRUD();

  const handleDeleteUser = useCallback(
    async (userID: string, userEmail: string) => {
      const confirmed = window.confirm(`Are you sure you want to delete user "${userEmail}"? This action cannot be undone.`);

      if (!confirmed) {
        return;
      }

      try {
        const result = await deleteUser(userID);

        if (!result.success) {
          const errorMessage = result.error || 'Failed to delete user';
          alert(`Error: ${errorMessage}`);
        }
      } catch (error) {
        console.error('Error deleting user:', error);
        alert('An unexpected error occurred while deleting the user.');
      }
    },
    [deleteUser],
  );

  const columns = [
    { key: UserTableColumn.Icon, title: '', sortable: true },
    { key: UserTableColumn.User, title: STRINGS.USERS_TAB, sortable: true },
    { key: UserTableColumn.Role, title: STRINGS.ROLE_COLUMN, sortable: true },
    { key: UserTableColumn.Teams, title: STRINGS.TEAMS_TAB, sortable: true },
    { key: UserTableColumn.Platforms, title: STRINGS.COMPUTE_PLATFORMS_TAB, sortable: true },
    { key: UserTableColumn.Actions, title: STRINGS.ACTIONS_COLUMN },
  ];

  const rows = useMemo(
    () =>
      users.map((user) => ({
        cells: [
          {
            columnKey: UserTableColumn.Icon,
            component: () => (
              <UserIconWrapper>
                <MinusIcon />
              </UserIconWrapper>
            ),
          },
          {
            columnKey: UserTableColumn.User,
            value: user.email,
          },
          {
            columnKey: UserTableColumn.Role,
            component: () => <RoleBadge variant={user.role}>{user.role}</RoleBadge>,
            value: user.role,
          },
          {
            columnKey: UserTableColumn.Teams,
            value: user.teams.length,
            component: () => (user.teams.length > 0 ? `${user.teams.length} ${STRINGS.TEAM_LABEL}${user.teams.length > 1 ? 's' : ''}` : STRINGS.DASH),
          },
          {
            columnKey: UserTableColumn.Platforms,
            value: user.computePlatforms.length,
            component: () => (user.computePlatforms.length > 0 ? `${user.computePlatforms.length} ${STRINGS.PLATFORM_LABEL}${user.computePlatforms.length > 1 ? 's' : ''}` : STRINGS.DASH),
          },
          {
            columnKey: UserTableColumn.Actions,
            component: () => (
              <div style={{ display: 'flex', gap: 12 }}>
                <ActionButton onClick={() => handleDeleteUser(user.id, user.email)} disabled={deleting} title='Delete user' aria-label={`Delete user ${user.email}`}>
                  <TrashIcon />
                </ActionButton>
              </div>
            ),
          },
        ] as RowCell[],
      })),
    [users, handleDeleteUser, deleting],
  );

  return <InteractiveTable columns={columns} rows={rows} />;
};
