import React, { useMemo, useState } from 'react';
import * as UTILS from '@/utils';
import { useUserStore } from '@/store';
import { IconCircle } from '../styled';
import type { ComputePlatform } from '@/types';
import { useTimeAgo } from '@odigos/ui-kit/hooks';
import { StatusType } from '@odigos/ui-kit/types';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import { TrashIcon, VIcon, XIcon } from '@odigos/ui-kit/icons';
import { useComputePlatformsCRUD, useProxy, useProxyCRUD } from '@/hooks';
import { useFilterStore, useNotificationStore } from '@odigos/ui-kit/store';
import { Badge, Table, TableVariant, WarningModal, type TableProps, DropDataOption } from '@odigos/ui-kit/components/v2';

export enum ConnectionsTableColumn {
  Id = 'id',
  Icon = 'icon',
  Name = 'name',
  Type = 'type',
  Status = 'status',
  OdigosVersion = 'odigosVersion',
  ConnectedSince = 'connectedSince',
  LastActivity = 'lastActivity',
}

export const ConnectionsTable: React.FC = () => {
  const { isAdmin } = useUserStore();
  const { selectProxy } = useProxy();
  const { formatTimeAgo } = useTimeAgo();
  const { deleteProxy, deleting } = useProxyCRUD();
  const { addNotification } = useNotificationStore();
  const { searchText, platformTypes, statuses } = useFilterStore();
  const { computePlatforms, loading, getComputePlatforms } = useComputePlatformsCRUD();

  const [deleteOpenForCp, setDeleteOpenForCp] = useState<ComputePlatform | null>(null);

  const onClickRow = (cp: ComputePlatform) => {
    if (cp.status === StatusType.Success) {
      selectProxy(cp.id);
    } else {
      addNotification({
        type: StatusType.Warning,
        title: UTILS.CONNECTION_LOST,
        message: 'Cannot connect to the platform',
        hideFromHistory: true,
      });
    }
  };

  const columns: TableProps['columns'] = useMemo(
    () => [
      { key: ConnectionsTableColumn.Icon, label: '', noSort: true, noFilter: true },
      { key: ConnectionsTableColumn.Name, label: UTILS.NAME },
      { key: ConnectionsTableColumn.Type, label: UTILS.TYPE },
      { key: ConnectionsTableColumn.Status, label: UTILS.STATUS },
      { key: ConnectionsTableColumn.OdigosVersion, label: 'Odigos Version' },
      { key: ConnectionsTableColumn.ConnectedSince, label: 'Connected Since' },
      { key: ConnectionsTableColumn.LastActivity, label: 'Last Activity' },
      { key: ConnectionsTableColumn.Id, label: UTILS.UNIQUE_ID, noSort: true, noFilter: true },
    ],
    [],
  );

  const filtered = useMemo(
    () =>
      computePlatforms.filter(
        ({ id, name, type, status }) =>
          (!searchText || id.toLowerCase().includes(searchText) || (name && name.toLowerCase().includes(searchText))) &&
          (!platformTypes?.length || platformTypes.find((opt: DropDataOption) => opt.id === type)) &&
          (!statuses?.length || statuses.find((opt: DropDataOption) => opt.id === status)),
      ),
    [computePlatforms, searchText, platformTypes, statuses],
  );

  const rows: TableProps['rows'] = useMemo(
    () =>
      filtered.map((cp) => {
        const PlatformIcon = getPlatformIcon(cp.type);
        const PlatformIconCell: React.FC = () => (
          <IconCircle>
            <PlatformIcon size={20} />
          </IconCircle>
        );
        const StatusCell: React.FC = () => {
          const label = cp.status === StatusType.Success ? UTILS.CONNECTION_ALIVE : UTILS.CONNECTION_LOST;
          const leftIcon = cp.status === StatusType.Success ? VIcon : XIcon;
          return <Badge status={cp.status} label={label} leftIcon={leftIcon} />;
        };
        const ConnectedSinceCell: React.FC = () => <span>{cp.status === StatusType.Success && cp.connectedAt ? formatTimeAgo(cp.connectedAt) : '—'}</span>;
        const LastActivityCell: React.FC = () => <span>{cp.lastSeenAt ? formatTimeAgo(cp.lastSeenAt) : '—'}</span>;

        return {
          onClick: () => onClickRow(cp),
          cells: [
            { key: ConnectionsTableColumn.Icon, rawValue: '', component: PlatformIconCell },
            { key: ConnectionsTableColumn.Name, rawValue: cp.name },
            { key: ConnectionsTableColumn.Type, rawValue: cp.type },
            { key: ConnectionsTableColumn.Status, rawValue: cp.status, component: StatusCell },
            { key: ConnectionsTableColumn.OdigosVersion, rawValue: cp.odigosVersion },
            { key: ConnectionsTableColumn.ConnectedSince, rawValue: cp.connectedAt || '', component: ConnectedSinceCell },
            { key: ConnectionsTableColumn.LastActivity, rawValue: cp.lastSeenAt || '', component: LastActivityCell },
            { key: ConnectionsTableColumn.Id, rawValue: cp.id },
          ],
        };
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [filtered, formatTimeAgo],
  );

  const getRowActions: TableProps['getRowActions'] = useMemo(() => {
    if (!isAdmin) return undefined;

    return ({ cells }) => {
      const id = cells.find((c) => c.key === ConnectionsTableColumn.Id)?.rawValue;
      const cp = filtered.find((p) => p.id === id);
      if (!cp || cp.status === StatusType.Success) return [];

      return [
        {
          id: `delete-${cp.id}`,
          tooltip: 'Delete connection',
          rightIcon: TrashIcon,
          loading: deleting,
          onClick: () => setDeleteOpenForCp(cp),
        },
      ];
    };
  }, [isAdmin, filtered, deleting]);

  return (
    <>
      <Table variant={TableVariant.Pretty} columns={columns} rows={rows} isLoading={loading} noDataProps={{ title: 'No data found' }} getRowActions={getRowActions} />

      <WarningModal
        isOpen={deleteOpenForCp !== null}
        title='Delete connection'
        description={`Are you sure you want to delete "${deleteOpenForCp?.name || deleteOpenForCp?.id || ''}"?`}
        onClose={() => setDeleteOpenForCp(null)}
        approveButton={{
          label: 'Delete',
          loading: deleting,
          onClick: () => {
            const proxyID = deleteOpenForCp?.id as string;
            deleteProxy(proxyID)
              .catch(() => {})
              .finally(() => {
                setDeleteOpenForCp(null);
                getComputePlatforms();
              });
          },
        }}
        denyButton={{
          label: 'Cancel',
          onClick: () => setDeleteOpenForCp(null),
        }}
      />
    </>
  );
};
