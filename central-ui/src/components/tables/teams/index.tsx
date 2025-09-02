import React, { useMemo, useCallback, useState } from 'react';
import * as UTILS from '@/utils';
import { useTeamsCRUD } from '@/hooks';
import Theme from '@odigos/ui-kit/theme';
import { EditTeamDrawer } from '@/components';
import type { Team, TeamInput } from '@/types';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { EditIcon, TrashIcon, UserGroupIcon } from '@odigos/ui-kit/icons';
import { CenterThis, DeleteWarning, FlexRow, IconButton, IconWrapped, InteractiveTable, NoDataFound, RowCell } from '@odigos/ui-kit/components';

export enum TeamsTableColumn {
  Icon = 'icon',
  Name = 'name',
  Description = 'description',
  Members = 'members',
  Platforms = 'platforms',
  Actions = 'actions',
}

export const TeamsTable: React.FC = () => {
  const theme = Theme.useTheme();
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

  const columns = [
    { key: TeamsTableColumn.Icon, title: '' },
    { key: TeamsTableColumn.Name, title: UTILS.NAME, sortable: true },
    { key: TeamsTableColumn.Description, title: UTILS.DESCRIPTION, sortable: true },
    { key: TeamsTableColumn.Members, title: UTILS.MEMBERS, sortable: true },
    { key: TeamsTableColumn.Platforms, title: UTILS.COMPUTE_PLATFORMS, sortable: true },
    { key: TeamsTableColumn.Actions, title: UTILS.ACTIONS },
  ];

  const rows = useMemo(
    () =>
      teams.map((team) => ({
        onClick: () => setEditOpenForTeam(team),
        cells: [
          {
            columnKey: TeamsTableColumn.Icon,
            component: () => <IconWrapped icon={() => <UserGroupIcon strokeWidth={2} size={24} />} />,
          },
          {
            columnKey: TeamsTableColumn.Name,
            value: team.name,
          },
          {
            columnKey: TeamsTableColumn.Description,
            value: team.description,
          },
          {
            columnKey: TeamsTableColumn.Members,
            value: UTILS.MEMBERS_COUNT(team.users?.length || 0),
          },
          {
            columnKey: TeamsTableColumn.Platforms,
            value: UTILS.PLATFORMS_COUNT(team.computePlatforms?.length || 0),
          },
          {
            columnKey: TeamsTableColumn.Actions,
            component: () => (
              <FlexRow>
                <IconButton onClick={() => setEditOpenForTeam(team)} tooltip='Edit team'>
                  <EditIcon />
                </IconButton>
                <IconButton onClick={() => setDeleteOpenForTeam(team)} tooltip='Delete team'>
                  <TrashIcon fill={theme.text.error} />
                </IconButton>
              </FlexRow>
            ),
          },
        ] as RowCell[],
      })),
    [teams, theme.text.error],
  );

  return (
    <>
      <InteractiveTable columns={columns} rows={rows} />
      {!rows.length && (
        <CenterThis style={{ marginTop: '2rem' }}>
          <NoDataFound />
        </CenterThis>
      )}

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

      <DeleteWarning
        isOpen={deleteOpenForTeam !== null}
        name={deleteOpenForTeam?.name || deleteOpenForTeam?.id}
        onApprove={() => {
          handleDeleteTeam(deleteOpenForTeam?.id as string);
          setDeleteOpenForTeam(null);
          setEditOpenForTeam(null);
        }}
        onDeny={() => {
          setDeleteOpenForTeam(null);
        }}
      />
    </>
  );
};
