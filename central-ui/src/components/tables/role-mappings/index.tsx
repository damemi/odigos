import React, { useCallback, useMemo, useState } from 'react';
import * as UTILS from '@/utils';
import { IconCircle } from '../styled';
import { useUserStore } from '@/store';
import styled, { useTheme } from 'styled-components';
import { useRoleMappingsCRUD } from '@/hooks';
import { EditRoleMappingDrawer } from '@/components';
import type { SVG } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import type { GroupRoleMapping, GroupRoleMappingInput } from '@/types';
import { EditIcon, RoleIcon, TrashIcon } from '@odigos/ui-kit/icons';
import { Tag, Table, TableVariant, Typography, TypographySize, WarningModal, type TableProps, type TableRow } from '@odigos/ui-kit/components';

// Group values are usually opaque IDs (e.g. Azure group object GUIDs); monospace
// makes them easier to scan and compare.
const GroupIdText = styled(Typography)`
  font-family: monospace;
`;

enum RoleMappingTableColumn {
  Icon = 'icon',
  DisplayName = 'displayName',
  Group = 'group',
  Role = 'role',
}

export const RoleMappingsTable: React.FC = () => {
  const theme = useTheme();
  const { isAdmin } = useUserStore();
  const { addNotification } = useNotificationStore();
  const { roleMappings, updateRoleMapping, updating, deleteRoleMapping, deleting, loading } = useRoleMappingsCRUD();

  const [editOpenFor, setEditOpenFor] = useState<GroupRoleMapping | null>(null);
  const [deleteOpenFor, setDeleteOpenFor] = useState<GroupRoleMapping | null>(null);

  const handleUpdate = useCallback(
    async (id: string, input: GroupRoleMappingInput) => {
      try {
        if (updating) return;
        const result = await updateRoleMapping(id, input);
        if (!result?.success) {
          addNotification({ type: StatusType.Error, title: Crud.Update, description: result?.error || 'Failed to update role mapping' });
        }
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : 'An unexpected error occurred while updating the role mapping.';
        addNotification({ type: StatusType.Error, title: Crud.Update, description: errorMessage });
      }
    },
    [updating, updateRoleMapping, addNotification],
  );

  const handleDelete = useCallback(
    async (id: string) => {
      try {
        if (deleting) return;
        const result = await deleteRoleMapping(id);
        if (!result?.success) {
          addNotification({ type: StatusType.Error, title: Crud.Delete, description: result?.error || 'Failed to delete role mapping' });
        }
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : 'An unexpected error occurred while deleting the role mapping.';
        addNotification({ type: StatusType.Error, title: Crud.Delete, description: errorMessage });
      }
    },
    [deleting, deleteRoleMapping, addNotification],
  );

  const columns: TableProps['columns'] = useMemo(
    () => [
      { key: RoleMappingTableColumn.Icon, label: '', noSort: true, noFilter: true },
      { key: RoleMappingTableColumn.DisplayName, label: STRINGS.DISPLAY_NAME, noSort: true, noFilter: true },
      { key: RoleMappingTableColumn.Group, label: STRINGS.GROUP, noSort: true, noFilter: true },
      { key: RoleMappingTableColumn.Role, label: STRINGS.ROLE, noSort: true, noFilter: true },
    ],
    [],
  );

  const rows: TableProps['rows'] = useMemo(
    () =>
      roleMappings.map((mapping) => {
        const IconCell = () => (
          <IconCircle>
            <RoleIcon size={20} />
          </IconCircle>
        );
        const DisplayNameCell = () => (
          <Typography size={TypographySize.XS} nowrap>
            {mapping.displayName || '-'}
          </Typography>
        );
        const GroupCell = () => (
          <GroupIdText size={TypographySize.XS} nowrap>
            {mapping.claimValue}
          </GroupIdText>
        );
        const RoleCell = () => <Tag variant={UTILS.getTagVariantForRole(mapping.role)} label={UTILS.ROLE_LABELS[mapping.role] ?? mapping.role} />;

        return {
          onClick: isAdmin ? () => setEditOpenFor(mapping) : undefined,
          cells: [
            { key: RoleMappingTableColumn.Icon, rawValue: '', component: IconCell },
            { key: RoleMappingTableColumn.DisplayName, rawValue: mapping.displayName ?? '', component: DisplayNameCell },
            { key: RoleMappingTableColumn.Group, rawValue: mapping.claimValue, component: GroupCell },
            { key: RoleMappingTableColumn.Role, rawValue: mapping.role, component: RoleCell },
          ],
        };
      }),
    [roleMappings, isAdmin],
  );

  const getRowActions: TableProps['getRowActions'] = useCallback(
    ({ cells }: TableRow) => {
      if (!isAdmin) return [];

      const groupRaw = cells.find((c) => c.key === RoleMappingTableColumn.Group)?.rawValue;
      const roleRaw = cells.find((c) => c.key === RoleMappingTableColumn.Role)?.rawValue;
      const mapping = roleMappings.find((m) => m.claimValue === groupRaw && m.role === roleRaw);
      if (!mapping) return [];

      const TrashFilled: SVG = (props) => <TrashIcon fill={theme.text.error} {...props} />;

      return [
        {
          id: `edit-${mapping.id}`,
          tooltip: STRINGS.EDIT_ROLE_MAPPING,
          rightIcon: EditIcon,
          onClick: () => setEditOpenFor(mapping),
        },
        {
          id: `delete-${mapping.id}`,
          tooltip: STRINGS.DELETE_ROLE_MAPPING,
          rightIcon: TrashFilled,
          onClick: () => setDeleteOpenFor(mapping),
        },
      ];
    },
    [isAdmin, roleMappings, theme.text.error],
  );

  return (
    <>
      <Table variant={TableVariant.Pretty} columns={columns} rows={rows} isLoading={loading} noDataProps={{ title: 'No data found' }} getRowActions={isAdmin ? getRowActions : undefined} />

      {editOpenFor && (
        <EditRoleMappingDrawer
          roleMapping={editOpenFor}
          onClose={() => setEditOpenFor(null)}
          onSave={(payload) => {
            handleUpdate(editOpenFor.id, payload);
            setEditOpenFor(null);
          }}
          onDelete={() => {
            setDeleteOpenFor(editOpenFor);
            setEditOpenFor(null);
          }}
        />
      )}

      <WarningModal
        isOpen={deleteOpenFor !== null}
        title={STRINGS.DELETE_ROLE_MAPPING}
        description={STRINGS.DELETE_ROLE_MAPPING_CONFIRM(deleteOpenFor?.displayName || deleteOpenFor?.claimValue || '')}
        onClose={() => setDeleteOpenFor(null)}
        approveButton={{
          label: STRINGS.DELETE,
          onClick: () => {
            handleDelete(deleteOpenFor?.id as string);
            setDeleteOpenFor(null);
            setEditOpenFor(null);
          },
        }}
        denyButton={{
          label: STRINGS.CANCEL,
          onClick: () => setDeleteOpenFor(null),
        }}
      />
    </>
  );
};
