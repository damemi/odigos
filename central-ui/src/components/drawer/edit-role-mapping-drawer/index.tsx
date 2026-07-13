import React, { useState, type FC } from 'react';
import styled from 'styled-components';
import { ROLE_OPTIONS } from '@/utils';
import { useUserStore } from '@/store';
import { KeyIcon } from '@odigos/ui-kit/icons';
import * as STRINGS from '@/utils/constants/strings';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { Role, type GroupRoleMapping, type GroupRoleMappingInput } from '@/types';
import { BUTTON_TEXTS } from '@odigos/ui-kit/constants';
import { FlexColumn, FlexRow } from '@odigos/ui-kit/components';
import { ButtonVariants, DataCard, DropDown, Drawer, Input, Typography, WarningModal } from '@odigos/ui-kit/components';

interface EditRoleMappingDrawerProps {
  roleMapping: GroupRoleMapping;
  onClose: () => void;
  onSave: (payload: GroupRoleMappingInput) => void;
  onDelete: () => void;
}

interface FormData {
  claimValue: string;
  claimName: string;
  role: Role;
  displayName: string;
}

const DataCardContentRow = styled(FlexRow)`
  width: 100%;
  gap: 36px;
  align-items: flex-start;
  flex-wrap: wrap;
`;

const DataCardContentCell = styled(FlexColumn)`
  line-height: 30px;
`;

const DataCardCellTitle = styled(Typography)`
  color: ${({ theme }) => theme.v2.colors.silver['400']};
  font-size: 12px;
`;

const DataCardCellStretch = styled.div`
  width: 300px;
`;

const EditRoleMappingDrawer: FC<EditRoleMappingDrawerProps> = ({ roleMapping, onClose, onSave, onDelete }) => {
  const { isAdmin } = useUserStore();
  const { formData, handleFormChange, resetFormData, isFormDirty } = useGenericForm<FormData>({
    claimValue: roleMapping.claimValue || '',
    claimName: roleMapping.claimName || STRINGS.CLAIM_NAME_PLACEHOLDER,
    role: roleMapping.role,
    displayName: roleMapping.displayName || '',
  });

  const [cancelWarningOpen, setCancelWarningOpen] = useState(false);
  const [cancelApproveCallback, setCancelApproveCallback] = useState<(() => void) | null>(null);

  const handleWithCancelWarning = (callback: () => void) => {
    if (isFormDirty) {
      setCancelWarningOpen(true);
      setCancelApproveCallback(() => callback);
    } else {
      callback();
    }
  };

  const handleSave = () => {
    onSave({
      providerId: roleMapping.providerId,
      claimValue: formData.claimValue,
      claimName: formData.claimName || undefined,
      role: formData.role,
      displayName: formData.displayName || undefined,
    });
  };

  return (
    <>
      <Drawer
        isOpen
        header={{
          onClose: () => handleWithCancelWarning(onClose),
          icon: KeyIcon,
          title: roleMapping.displayName || roleMapping.claimValue,
          actions: isAdmin
            ? [
                {
                  id: 'delete-role-mapping',
                  label: STRINGS.DELETE_MAPPING,
                  onClick: onDelete,
                },
              ]
            : [],
        }}
        footer={{
          actions:
            isAdmin && isFormDirty
              ? [
                  {
                    'data-id': 'drawer-cancel',
                    variant: ButtonVariants.Secondary,
                    label: BUTTON_TEXTS.CANCEL,
                    onClick: () => handleWithCancelWarning(resetFormData),
                  },
                  {
                    'data-id': 'drawer-save',
                    variant: ButtonVariants.Primary,
                    label: BUTTON_TEXTS.SAVE,
                    onClick: handleSave,
                  },
                ]
              : [],
        }}
      >
        <FlexColumn $gap={24}>
          <DataCard>
            <DataCardContentRow>
              <DataCardContentCell>
                <DataCardCellTitle>{STRINGS.DISPLAY_NAME}</DataCardCellTitle>
                <DataCardCellStretch>
                  <Input value={formData.displayName} disabled={!isAdmin} onChange={(e) => handleFormChange('displayName', e.target.value)} placeholder={STRINGS.DISPLAY_NAME_PLACEHOLDER} />
                </DataCardCellStretch>
              </DataCardContentCell>
              <DataCardContentCell>
                <DataCardCellTitle>{STRINGS.CLAIM_VALUE}</DataCardCellTitle>
                <DataCardCellStretch>
                  <Input value={formData.claimValue} disabled={!isAdmin} onChange={(e) => handleFormChange('claimValue', e.target.value)} placeholder={STRINGS.CLAIM_VALUE_PLACEHOLDER} />
                </DataCardCellStretch>
              </DataCardContentCell>
              <DataCardContentCell>
                <DataCardCellTitle>{STRINGS.ROLE}</DataCardCellTitle>
                <DataCardCellStretch>
                  <DropDown
                    placeholder={STRINGS.ROLE}
                    options={ROLE_OPTIONS}
                    disabled={!isAdmin}
                    values={[formData.role]}
                    setValues={([value]) => handleFormChange('role', (value as Role) ?? Role.VIEWER)}
                    required
                  />
                </DataCardCellStretch>
              </DataCardContentCell>
              <DataCardContentCell>
                <DataCardCellTitle>{STRINGS.CLAIM_NAME}</DataCardCellTitle>
                <DataCardCellStretch>
                  <Input value={formData.claimName} disabled={!isAdmin} onChange={(e) => handleFormChange('claimName', e.target.value)} placeholder={STRINGS.CLAIM_NAME_PLACEHOLDER} />
                </DataCardCellStretch>
              </DataCardContentCell>
            </DataCardContentRow>
          </DataCard>
        </FlexColumn>
      </Drawer>

      <WarningModal
        isOpen={cancelWarningOpen}
        title={STRINGS.DISCARD_CHANGES_TITLE}
        description={STRINGS.DISCARD_CHANGES_DESCRIPTION}
        onClose={() => setCancelWarningOpen(false)}
        approveButton={{
          label: STRINGS.APPROVE,
          onClick: () => {
            cancelApproveCallback?.();
            setCancelWarningOpen(false);
          },
        }}
        denyButton={{
          label: STRINGS.DENY,
          onClick: () => {
            setCancelWarningOpen(false);
          },
        }}
      />
    </>
  );
};

export { EditRoleMappingDrawer, type EditRoleMappingDrawerProps };
