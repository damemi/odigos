import React, { useCallback, useMemo, useState } from 'react';
import * as UTILS from '@/utils';
import { IconCircle } from '../styled';
import { useUserStore } from '@/store';
import { useTeamsCRUD } from '@/hooks';
import { useTheme } from 'styled-components';
import { EditTeamDrawer } from '@/components';
import type { Team, TeamInput } from '@/types';
import type { SVG } from '@odigos/ui-kit/types';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { EditIcon, TrashIcon, UserGroupIcon } from '@odigos/ui-kit/icons';
import { Table, TableVariant, WarningModal, type TableProps, type TableRow } from '@odigos/ui-kit/components/v2';

export enum TeamsTableColumn {
  Icon = 'icon',
  Name = 'name',
  Description = 'description',
  Members = 'members',
  Platforms = 'platforms',
}

export const TeamsTable: React.FC = () => {
  const theme = useTheme();
  const { isAdmin } = useUserStore();
  const { addNotification } = useNotificationStore();
  const { teams, updateTeam, updating, deleteTeam, deleting } = useTeamsCRUD();

  const [editOpenForTeam, setEditOpenForTeam] = useState<Team | null>(null);
  const [deleteOpenForTeam, setDeleteOpenForTeam] = useState<Team | null>(null);

  const handleUpdateTeam = useCallback(
    async (teamID: string, input: TeamInput) => {
      try {
        if (updating) return;
        const result = await updateTeam(teamID, input);

        if (!result?.success) {
          const errorMessage = result?.error || 'Failed to update team';
          addNotification({ type: StatusType.Error, title: Crud.Update, description: errorMessage });
        }
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : 'An unexpected error occurred while updating the team.';
        addNotification({ type: StatusType.Error, title: Crud.Update, description: errorMessage });
      }
    },
    [updating, updateTeam, addNotification],
  );

  const handleDeleteTeam = useCallback(
    async (teamID: string) => {
      try {
        if (deleting) return;
        const result = await deleteTeam(teamID);

        if (!result?.success) {
          const errorMessage = result?.error || 'Failed to delete team';
          addNotification({ type: StatusType.Error, title: Crud.Delete, description: errorMessage });
        }
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : 'An unexpected error occurred while deleting the team.';
        addNotification({ type: StatusType.Error, title: Crud.Delete, description: errorMessage });
      }
    },
    [deleting, deleteTeam, addNotification],
  );

  const columns: TableProps['columns'] = useMemo(
    () => [
      { key: TeamsTableColumn.Icon, label: '', noSort: true, noFilter: true },
      { key: TeamsTableColumn.Name, label: UTILS.NAME },
      { key: TeamsTableColumn.Description, label: UTILS.DESCRIPTION },
      { key: TeamsTableColumn.Members, label: UTILS.MEMBERS },
      { key: TeamsTableColumn.Platforms, label: UTILS.COMPUTE_PLATFORMS },
    ],
    [],
  );

  const rows: TableProps['rows'] = useMemo(
    () =>
      teams.map((team) => {
        const TeamIconCell: React.FC = () => (
          <IconCircle>
            <UserGroupIcon strokeWidth={2} size={20} />
          </IconCircle>
        );

        return {
          onClick: () => setEditOpenForTeam(team),
          cells: [
            { key: TeamsTableColumn.Icon, rawValue: '', component: TeamIconCell },
            { key: TeamsTableColumn.Name, rawValue: team.name },
            { key: TeamsTableColumn.Description, rawValue: team.description ?? '' },
            { key: TeamsTableColumn.Members, rawValue: UTILS.MEMBERS_COUNT(team.users?.length || 0) },
            { key: TeamsTableColumn.Platforms, rawValue: UTILS.PLATFORMS_COUNT(team.computePlatforms?.length || 0) },
          ],
        };
      }),
    [teams],
  );

  const getRowActions: TableProps['getRowActions'] = useCallback(
    ({ cells }: TableRow) => {
      if (!isAdmin) return [];

      const team = teams.find((t) => t.name === cells.find((c) => c.key === TeamsTableColumn.Name)?.rawValue);
      if (!team) return [];

      const TrashFilled: SVG = (props) => <TrashIcon fill={theme.text.error} {...props} />;

      return [
        {
          id: `edit-${team.id}`,
          tooltip: 'Edit team',
          rightIcon: EditIcon,
          onClick: () => setEditOpenForTeam(team),
        },
        {
          id: `delete-${team.id}`,
          tooltip: 'Delete team',
          rightIcon: TrashFilled,
          onClick: () => setDeleteOpenForTeam(team),
        },
      ];
    },
    [isAdmin, teams, theme.text.error],
  );

  return (
    <>
      <Table variant={TableVariant.Pretty} columns={columns} rows={rows} noDataProps={{ title: 'No data found' }} getRowActions={isAdmin ? getRowActions : undefined} />

      {editOpenForTeam && (
        <EditTeamDrawer
          team={editOpenForTeam}
          onClose={() => {
            setEditOpenForTeam(null);
          }}
          onSave={(payload) => {
            handleUpdateTeam(editOpenForTeam.id, payload);
            setEditOpenForTeam(null);
          }}
          onDelete={() => {
            setDeleteOpenForTeam(editOpenForTeam);
            setEditOpenForTeam(null);
          }}
        />
      )}

      <WarningModal
        isOpen={deleteOpenForTeam !== null}
        title='Delete team'
        description={`Are you sure you want to delete "${deleteOpenForTeam?.name || deleteOpenForTeam?.id || ''}"?`}
        onClose={() => setDeleteOpenForTeam(null)}
        approveButton={{
          label: 'Delete',
          onClick: () => {
            handleDeleteTeam(deleteOpenForTeam?.id as string);
            setDeleteOpenForTeam(null);
            setEditOpenForTeam(null);
          },
        }}
        denyButton={{
          label: 'Cancel',
          onClick: () => setDeleteOpenForTeam(null),
        }}
      />
    </>
  );
};
