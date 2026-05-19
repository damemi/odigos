import React, { useCallback, useMemo, useState } from 'react';
import * as UTILS from '@/utils';
import { IconCircle } from '../styled';
import { useUserStore } from '@/store';
import { EditIcon } from '@odigos/ui-kit/icons';
import { useComputePlatformsCRUD } from '@/hooks';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import { useNotificationStore } from '@odigos/ui-kit/store';
import type { ComputePlatform, ComputePlatformInput } from '@/types';
import { EditComputePlatformDrawer } from '@/components/drawer/edit-compute-platform-drawer';
import { Table, TableVariant, type TableProps, type TableRow } from '@odigos/ui-kit/components/v2';

export enum ComputePlatformsTableColumn {
  Icon = 'icon',
  Name = 'name',
  Type = 'type',
  Members = 'members',
  Teams = 'teams',
}

export const ComputePlatformsTable: React.FC = () => {
  const { isAdmin } = useUserStore();
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

  const columns: TableProps['columns'] = useMemo(
    () => [
      { key: ComputePlatformsTableColumn.Icon, label: '', noSort: true, noFilter: true },
      { key: ComputePlatformsTableColumn.Name, label: UTILS.NAME },
      { key: ComputePlatformsTableColumn.Type, label: UTILS.TYPE },
      // TODO: uncomment when user attributes are implemented
      // { key: ComputePlatformsTableColumn.Members, label: UTILS.MEMBERS },
      { key: ComputePlatformsTableColumn.Teams, label: UTILS.TEAMS },
    ],
    [],
  );

  const rows: TableProps['rows'] = useMemo(
    () =>
      computePlatforms.map((cp) => {
        const PlatformIcon = getPlatformIcon(cp.type);
        const PlatformIconCell: React.FC = () => (
          <IconCircle>
            <PlatformIcon size={20} />
          </IconCircle>
        );

        return {
          onClick: () => setEditOpenForCP(cp),
          cells: [
            { key: ComputePlatformsTableColumn.Icon, rawValue: '', component: PlatformIconCell },
            { key: ComputePlatformsTableColumn.Name, rawValue: cp.name },
            { key: ComputePlatformsTableColumn.Type, rawValue: cp.type },
            // TODO: uncomment when user attributes are implemented
            // { key: ComputePlatformsTableColumn.Members, rawValue: UTILS.MEMBERS_COUNT(cp.users?.length || 0) },
            { key: ComputePlatformsTableColumn.Teams, rawValue: UTILS.TEAMS_COUNT(cp.teams?.length || 0) },
          ],
        };
      }),
    [computePlatforms],
  );

  const getRowActions: TableProps['getRowActions'] = useCallback(
    ({ cells }: TableRow) => {
      if (!isAdmin) return [];

      const cp = computePlatforms.find((c) => c.name === cells.find((cell) => cell.key === ComputePlatformsTableColumn.Name)?.rawValue);
      if (!cp) return [];

      return [
        {
          id: `edit-${cp.id}`,
          tooltip: 'Edit compute platform',
          rightIcon: EditIcon,
          onClick: () => setEditOpenForCP(cp),
        },
      ];
    },
    [isAdmin, computePlatforms],
  );

  return (
    <>
      <Table variant={TableVariant.Pretty} columns={columns} rows={rows} noDataProps={{ title: 'No data found' }} getRowActions={isAdmin ? getRowActions : undefined} />

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
