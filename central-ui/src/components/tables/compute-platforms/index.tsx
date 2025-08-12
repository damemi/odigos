import React, { useMemo, useCallback, useState, useEffect } from 'react';
import * as UTILS from '@/utils';
import type { Connection } from '@/types';
import { useConnectionsCRUD } from '@/hooks';
import { EditIcon } from '@odigos/ui-kit/icons';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import { useNotificationStore } from '@odigos/ui-kit/store';
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
  const { connections } = useConnectionsCRUD();

  // TODO: develop this in `useConnectionsCRUD` hook
  const updating = false;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const updateCP = async (...params: any[]) => {
    return { success: false, error: 'TODO development' };
  };

  const [editOpenForCP, setEditOpenForCP] = useState<Connection | null>(null);

  // TODO: remove this effect after developing the handler
  useEffect(() => {
    if (editOpenForCP) {
      alert('TODO development: editOpenForCP');
      setEditOpenForCP(null);
    }
  }, [editOpenForCP]);

  const handleUpdateCP = useCallback(
    async (proxyID: string, input: unknown) => {
      try {
        if (updating) return;
        const result = await updateCP(proxyID, input);

        if (!result?.success) {
          const errorMessage = result?.error || 'Failed to update compute platform';
          addNotification({ type: StatusType.Error, title: Crud.Update, description: errorMessage });
        }
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : 'An unexpected error occurred while updating the compute platform.';
        addNotification({ type: StatusType.Error, title: Crud.Update, description: errorMessage });
      }
    },
    [updating, updateCP, addNotification],
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
      connections.map((connection) => ({
        onClick: () => setEditOpenForCP(connection),
        cells: [
          {
            columnKey: ComputePlatformsTableColumn.Icon,
            component: () => (
              <IconWrapped
                icon={() =>
                  getPlatformIcon(connection.type)({
                    strokeWidth: 2,
                    size: 24,
                  })
                }
              />
            ),
          },
          {
            columnKey: ComputePlatformsTableColumn.Name,
            value: connection.name,
          },
          {
            columnKey: ComputePlatformsTableColumn.Type,
            value: connection.type,
          },
          {
            columnKey: ComputePlatformsTableColumn.Members,
            // TODO: populate this with the number of members
            value: UTILS.MEMBERS_COUNT(0),
          },
          {
            columnKey: ComputePlatformsTableColumn.Teams,
            // TODO: populate this with the number of teams
            value: UTILS.TEAMS_COUNT(0),
          },
          {
            columnKey: ComputePlatformsTableColumn.Actions,
            component: () => (
              <FlexRow>
                <IconButton onClick={() => setEditOpenForCP(connection)} tooltip='Edit compute platform'>
                  <EditIcon />
                </IconButton>
              </FlexRow>
            ),
          },
        ] as RowCell[],
      })),
    [connections],
  );

  return (
    <>
      <InteractiveTable columns={columns} rows={rows} />
      {!rows.length && (
        <CenterThis style={{ marginTop: '2rem' }}>
          <NoDataFound />
        </CenterThis>
      )}

      {/* TODO: create the drawer */}
      {/* {editOpenForCP && (
        <EditComputePlatformDrawer
          computePlatform={editOpenForCP}
          onClose={() => {
            setEditOpenForCP(null);
          }}
          onSave={(payload) => {
            handleUpdateCP(editOpenForCP.id, {
              name: payload.name,
              description: payload.description,
              users: payload.users?.map((user) => user.id) || [],
              computePlatforms: payload.computePlatforms?.map((platform) => platform.id) || [],
            });
            setEditOpenForCP(null);
          }}
          onDelete={() => {
            setDeleteOpenForCP(editOpenForCP);
            setEditOpenForCP(null);
          }}
        />
      )} */}
    </>
  );
};
