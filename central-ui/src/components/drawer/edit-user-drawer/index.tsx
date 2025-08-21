import React, { useState, type FC } from 'react';
import type { User, UserInput } from '@/types';
import styled from 'styled-components';
import { UserCard } from './user-card';
import Theme from '@odigos/ui-kit/theme';
import { Relative } from '@/components/styled';
import { TeamsCard } from '../cards/teams-card';
import { BUTTON_TEXTS } from '@odigos/ui-kit/constants';
import { useGenericForm, usePopup } from '@odigos/ui-kit/hooks';
import { RotateArrowsIcon, TrashIcon, UserIcon } from '@odigos/ui-kit/icons';
import { CancelWarning, Drawer, FlexColumn, IconButton, Popup, Text } from '@odigos/ui-kit/components';

interface EditUserDrawerProps {
  user: User;
  onClose: () => void;
  onSave: (payload: UserInput) => void;
  onDelete: () => void;
  onResetPassword: () => void;
}

const ActionItem = styled(Text)`
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  &:hover {
    text-decoration: underline;
  }
`;

const DRAWER_WIDTH = '750px';

const EditUserDrawer: FC<EditUserDrawerProps> = ({ user, onClose, onSave, onDelete, onResetPassword }) => {
  const theme = Theme.useTheme();
  const { popupRef, popupOpen, setPopupOpen } = usePopup();
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
        width={DRAWER_WIDTH}
        isOpen
        onClose={() => handleWithCancelWarning(onClose)}
        header={{
          icons: [UserIcon],
          title: user.email,
          actions: [
            <Relative key='actions'>
              <IconButton withBorder onClick={() => setPopupOpen((prev) => !prev)}>
                <Text size={10}>•••</Text>
              </IconButton>
              <Popup ref={popupRef} isOpen={popupOpen} top={36} right={0} padding='24px'>
                <FlexColumn $gap={12}>
                  <ActionItem size={14} onClick={onResetPassword}>
                    <RotateArrowsIcon /> Reset Password
                  </ActionItem>
                  <ActionItem size={14} color={theme.text.error} onClick={onDelete}>
                    <TrashIcon /> Delete User
                  </ActionItem>
                </FlexColumn>
              </Popup>
            </Relative>,
          ],
        }}
        footer={{
          isOpen: isFormDirty,
          centerButtons: [
            {
              'data-id': 'drawer-cancel',
              variant: 'secondary',
              onClick: () => handleWithCancelWarning(resetFormData),
              children: BUTTON_TEXTS.CANCEL,
            },
            {
              'data-id': 'drawer-save',
              variant: 'primary',
              onClick: () => onSave(formData),
              children: BUTTON_TEXTS.SAVE,
            },
          ],
        }}
      >
        <FlexColumn $gap={24}>
          <UserCard user={user} formData={formData} handleFormChange={handleFormChange} />
          <TeamsCard currentTeams={user.teams || []} formData={formData} handleFormChange={handleFormChange} />
        </FlexColumn>
      </Drawer>

      <CancelWarning
        isOpen={cancelWarningOpen}
        name={user.email}
        onApprove={() => {
          cancelApproveCallback?.();
          setCancelWarningOpen(false);
        }}
        onDeny={() => {
          setCancelWarningOpen(false);
        }}
      />
    </>
  );
};

export { EditUserDrawer, type EditUserDrawerProps };
