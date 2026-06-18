import React, { useState, type FC } from 'react';
import { UserCard } from './user-card';
import { useUserStore } from '@/store';
import type { User, UserInput } from '@/types';
import { UserIcon } from '@odigos/ui-kit/icons';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { FlexColumn } from '@odigos/ui-kit/components';
import { BUTTON_TEXTS } from '@odigos/ui-kit/constants';
import { ButtonVariants, Drawer, WarningModal } from '@odigos/ui-kit/components/v2';

interface EditUserDrawerProps {
  user: User;
  onClose: () => void;
  onSave: (payload: UserInput) => void;
  onDelete: () => void;
  onResetPassword: () => void;
}

const EditUserDrawer: FC<EditUserDrawerProps> = ({ user, onClose, onSave, onDelete, onResetPassword }) => {
  // User management (edit role / delete / reset password) is restricted to ADMIN/OWNER.
  // MAINTAINER and VIEWER can open the drawer to view details, but cannot modify anything.
  const { isAdmin } = useUserStore();
  const { formData, handleFormChange, resetFormData, isFormDirty } = useGenericForm({
    email: user.email || '',
    role: user.role,
    addTeamIds: [],
    removeTeamIds: [],
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

  return (
    <>
      <Drawer
        isOpen
        header={{
          onClose: () => handleWithCancelWarning(onClose),
          icon: UserIcon,
          title: user.email,
          actions: isAdmin
            ? [
                {
                  id: 'reset-password',
                  label: 'Reset Password',
                  onClick: onResetPassword,
                },
                {
                  id: 'delete-user',
                  label: 'Delete User',
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
                    onClick: () => onSave(formData),
                  },
                ]
              : [],
        }}
      >
        <FlexColumn $gap={24}>
          <UserCard user={user} formData={formData} handleFormChange={handleFormChange} />
        </FlexColumn>
      </Drawer>

      <WarningModal
        isOpen={cancelWarningOpen}
        title={`Discard changes?`}
        description={`You have unsaved changes.\nIf you cancel now, your changes won't be saved.`}
        onClose={() => setCancelWarningOpen(false)}
        approveButton={{
          label: 'Approve',
          onClick: () => {
            cancelApproveCallback?.();
            setCancelWarningOpen(false);
          },
        }}
        denyButton={{
          label: 'Deny',
          onClick: () => {
            setCancelWarningOpen(false);
          },
        }}
      />
    </>
  );
};

export { EditUserDrawer, type EditUserDrawerProps };
