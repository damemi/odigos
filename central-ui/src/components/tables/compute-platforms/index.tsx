import React, { useMemo, useCallback, useState } from 'react';
import * as UTILS from '@/utils';
import { useComputePlatformsCRUD } from '@/hooks';
import { EditIcon } from '@odigos/ui-kit/icons';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import { useNotificationStore } from '@odigos/ui-kit/store';
import type { ComputePlatform, ComputePlatformInput } from '@/types';
import { EditComputePlatformDrawer } from '@/components/drawer/edit-compute-platform-drawer';
import { CenterThis, FlexRow, IconButton, IconWrapped, InteractiveTable, NoDataFound, RowCell } from '@odigos/ui-kit/components';

export enum ComputePlatformsTableColumn {
  Icon = 'icon',
  Name = 'name',
  Type = 'type',
  Members = 'members',
  Teams = 'teams',
  Actions = 'actions',
}

export const ComputePlatformsTable: React.FC = () => {
  const { addNotification } = useNotificationStore();
  const { computePlatforms, updateComputePlatform, updating } = useComputePlatformsCRUD();

  const [editOpenForCP, setEditOpenForCP] = useState<ComputePlatform | null>(null);

  const handleUpdateCP = useCallback(
    async (id: string, input: ComputePlatformInput) => {
      try {
        if (updating) return;
        const result = await updateComputePlatform(id, input);

        if (!result?.success) {
          const errorMessage = result?.error || 'Failed to update compute platform';
          addNotification({ type: StatusType.Error, title: Crud.Update, description: errorMessage });
        }
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : 'An unexpected error occurred while updating the compute platform.';
        addNotification({ type: StatusType.Error, title: Crud.Update, description: errorMessage });
      }
    },
    [updating, updateComputePlatform, addNotification],
  );

  const columns = [
    { key: ComputePlatformsTableColumn.Icon, title: '' },
    { key: ComputePlatformsTableColumn.Name, title: UTILS.NAME, sortable: true },
    { key: ComputePlatformsTableColumn.Type, title: UTILS.TYPE, sortable: true },
    { key: ComputePlatformsTableColumn.Members, title: UTILS.MEMBERS_LABEL, sortable: true },
    { key: ComputePlatformsTableColumn.Teams, title: UTILS.TEAMS_TAB, sortable: true },
    { key: ComputePlatformsTableColumn.Actions, title: UTILS.ACTIONS_COLUMN },
  ];

  const rows = useMemo(
    () =>
      computePlatforms.map((cp) => ({
        onClick: () => setEditOpenForCP(cp),
        cells: [
          {
            columnKey: ComputePlatformsTableColumn.Icon,
            component: () => <IconWrapped icon={() => getPlatformIcon(cp.type)({ size: 24 })} />,
          },
          {
            columnKey: ComputePlatformsTableColumn.Name,
            value: cp.name,
          },
          {
            columnKey: ComputePlatformsTableColumn.Type,
            value: cp.type,
          },
          {
            columnKey: ComputePlatformsTableColumn.Members,
            value: UTILS.MEMBERS_COUNT(cp.users?.length || 0),
          },
          {
            columnKey: ComputePlatformsTableColumn.Teams,
            value: UTILS.TEAMS_COUNT(cp.teams?.length || 0),
          },
          {
            columnKey: ComputePlatformsTableColumn.Actions,
            component: () => (
              <FlexRow>
                <IconButton onClick={() => setEditOpenForCP(cp)} tooltip='Edit compute platform'>
                  <EditIcon />
                </IconButton>
              </FlexRow>
            ),
          },
        ] as RowCell[],
      })),
    [computePlatforms],
  );

  return (
    <>
      <InteractiveTable columns={columns} rows={rows} />
      {!rows.length && (
        <CenterThis style={{ marginTop: '2rem' }}>
          <NoDataFound />
        </CenterThis>
      )}

      {editOpenForCP && (
        <EditComputePlatformDrawer
          computePlatform={editOpenForCP}
          onClose={() => {
            setEditOpenForCP(null);
          }}
          onSave={(payload) => {
            handleUpdateCP(editOpenForCP.id, payload);
            setEditOpenForCP(null);
          }}
        />
      )}
    </>
  );
};
