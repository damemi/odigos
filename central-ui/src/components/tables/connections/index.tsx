import React, { useMemo, useState } from 'react';
import * as UTILS from '@/utils';
import { useUserStore } from '@/store';
import type { ComputePlatform } from '@/types';
import { TrashIcon } from '@odigos/ui-kit/icons';
import { StatusType } from '@odigos/ui-kit/types';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import { useComputePlatformsCRUD, useProxyCRUD, useProxyID } from '@/hooks';
import { useFilterStore, useNotificationStore } from '@odigos/ui-kit/store';
import { CenterThis, DeleteWarning, DropdownOption, FadeLoader, FlexRow, IconButton, IconWrapped, InteractiveTable, NoDataFound, RowCell, Status } from '@odigos/ui-kit/components';

export enum ConnectionsTableColumn {
  Id = 'id',
  Icon = 'icon',
  Name = 'name',
  Type = 'type',
  Status = 'status',
  OdigosVersion = 'odigosVersion',
  Actions = 'actions',
}

export const ConnectionsTable: React.FC = () => {
  const { isAdmin } = useUserStore();
  const { selectProxyID } = useProxyID();
  const { addNotification } = useNotificationStore();
  const { computePlatforms, loading, refetch } = useComputePlatformsCRUD();
  const { searchText, platformTypes, statuses } = useFilterStore();

  const [deleteOpenForCp, setDeleteOpenForCp] = useState<ComputePlatform | null>(null);

  const { deleteProxy, deleting } = useProxyCRUD();

  const onClickRow = ({ id, status }: ComputePlatform) => {
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

  const columns = useMemo(() => {
    const arr = [
      { key: ConnectionsTableColumn.Icon, title: '' },
      { key: ConnectionsTableColumn.Name, title: UTILS.NAME, sortable: true },
      { key: ConnectionsTableColumn.Type, title: UTILS.TYPE, sortable: true },
      { key: ConnectionsTableColumn.Status, title: UTILS.STATUS, sortable: true },
      { key: ConnectionsTableColumn.OdigosVersion, title: 'Odigos Version', sortable: true },
      { key: ConnectionsTableColumn.Id, title: UTILS.UNIQUE_ID },
    ];
    if (isAdmin) arr.push({ key: ConnectionsTableColumn.Actions, title: UTILS.ACTIONS });
    return arr;
  }, [isAdmin]);

  const filtered = useMemo(
    () =>
      computePlatforms.filter(
        ({ id, type, status }) =>
          (!searchText || id.toLowerCase().includes(searchText)) &&
          (!platformTypes?.length || platformTypes.find((opt: DropdownOption) => opt.id === type)) &&
          (!statuses?.length || statuses.find((opt: DropdownOption) => opt.id === status)),
      ),
    [computePlatforms, searchText, platformTypes, statuses],
  );

  const rows = useMemo(
    () =>
      filtered.map((cp) => ({
        onClick: () => onClickRow(cp),
        cells: [
          {
            columnKey: ConnectionsTableColumn.Icon,
            component: () => <IconWrapped icon={() => getPlatformIcon(cp.type)({ size: 24 })} />,
          },
          {
            columnKey: ConnectionsTableColumn.Id,
            value: cp.id,
          },
          {
            columnKey: ConnectionsTableColumn.Name,
            value: cp.name,
          },
          {
            columnKey: ConnectionsTableColumn.Type,
            value: cp.type,
          },
          {
            columnKey: ConnectionsTableColumn.OdigosVersion,
            value: cp.odigosVersion,
          },
          {
            columnKey: ConnectionsTableColumn.Status,
            component: () => (
              <div style={{ lineHeight: 1 }}>
                <Status status={cp.status} title={cp.status === StatusType.Success ? UTILS.CONNECTION_ALIVE : UTILS.CONNECTION_LOST} withIcon withBorder />
              </div>
            ),
          },
        ] as RowCell[],
      })),
    [filtered],
  );

  if (isAdmin) {
    rows.forEach((row, idx) => {
      const cp = filtered[idx];
      (row.cells as RowCell[]).push({
        columnKey: ConnectionsTableColumn.Actions,
        component: () => (
          <FlexRow>
            {deleting ? (
              <FadeLoader />
            ) : (
              <IconButton
                tooltip='Delete connection'
                onClick={(e) => {
                  e.stopPropagation();
                  setDeleteOpenForCp(cp);
                }}
              >
                <TrashIcon />
              </IconButton>
            )}
          </FlexRow>
        ),
      });
    });
  }

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
        <>
          <InteractiveTable columns={columns} rows={rows} />
          <DeleteWarning
            isOpen={deleteOpenForCp !== null}
            name={deleteOpenForCp?.name || deleteOpenForCp?.id}
            onApprove={() => {
              const proxyID = deleteOpenForCp?.id as string;
              deleteProxy(proxyID)
                .catch(() => {})
                .finally(() => {
                  setDeleteOpenForCp(null);
                  refetch();
                });
            }}
            onDeny={() => {
              setDeleteOpenForCp(null);
            }}
          />
        </>
      )}
    </>
  );
};
