import React, { useMemo } from 'react';
import * as UTILS from '@/utils';
import type { Connection } from '@/types';
import { StatusType } from '@odigos/ui-kit/types';
import { useConnectionsCRUD, useProxyID } from '@/hooks';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import { useFilterStore, useNotificationStore } from '@odigos/ui-kit/store';
import { CenterThis, DropdownOption, FadeLoader, IconWrapped, InteractiveTable, NoDataFound, RowCell, Status } from '@odigos/ui-kit/components';

export enum ConnectionsTableColumn {
  Id = 'id',
  Icon = 'icon',
  Name = 'name',
  Type = 'type',
  Status = 'status',
}

export const ConnectionsTable: React.FC = () => {
  const { selectProxyID } = useProxyID();
  const { addNotification } = useNotificationStore();
  const { connections, loading } = useConnectionsCRUD();
  const { searchText, platformTypes, statuses } = useFilterStore();

  const onClickRow = ({ id, status }: Connection) => {
    if (status === StatusType.Success) {
      selectProxyID(id);
    } else {
      addNotification({
        type: StatusType.Warning,
        title: UTILS.CONNECTION_LOST,
        message: 'Cannot connect to the platform',
        hideFromHistory: true,
      });
    }
  };

  const columns = [
    { key: ConnectionsTableColumn.Icon, title: '' },
    { key: ConnectionsTableColumn.Name, title: UTILS.NAME, sortable: true },
    { key: ConnectionsTableColumn.Type, title: UTILS.TYPE, sortable: true },
    { key: ConnectionsTableColumn.Status, title: UTILS.STATUS, sortable: true },
    { key: ConnectionsTableColumn.Id, title: UTILS.UNIQUE_ID },
  ];

  const filtered = useMemo(
    () =>
      connections.filter(
        ({ id, type, status }) =>
          (!searchText || id.toLowerCase().includes(searchText)) &&
          (!platformTypes?.length || platformTypes.find((opt: DropdownOption) => opt.id === type)) &&
          (!statuses?.length || statuses.find((opt: DropdownOption) => opt.id === status)),
      ),
    [connections, searchText, platformTypes, statuses],
  );

  const rows = useMemo(
    () =>
      filtered.map((connection) => ({
        onClick: () => onClickRow(connection),
        cells: [
          {
            columnKey: ConnectionsTableColumn.Icon,
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
            columnKey: ConnectionsTableColumn.Id,
            value: connection.id,
          },
          {
            columnKey: ConnectionsTableColumn.Name,
            value: connection.name,
          },
          {
            columnKey: ConnectionsTableColumn.Type,
            value: connection.type,
          },
          {
            columnKey: ConnectionsTableColumn.Status,
            component: () => (
              <div style={{ lineHeight: 1 }}>
                <Status status={connection.status} title={connection.status === StatusType.Success ? UTILS.CONNECTION_ALIVE : UTILS.CONNECTION_LOST} withIcon withBorder />
              </div>
            ),
          },
        ] as RowCell[],
      })),
    [filtered],
  );

  return (
    <>
      {loading ? (
        <CenterThis style={{ marginTop: '2rem' }}>
          <FadeLoader />
        </CenterThis>
      ) : !rows.length ? (
        <CenterThis style={{ marginTop: '2rem' }}>
          <NoDataFound />
        </CenterThis>
      ) : (
        <InteractiveTable columns={columns} rows={rows} />
      )}
    </>
  );
};
