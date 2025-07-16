import React, { type FC } from 'react';
import type { User } from '@/types';
import styled from 'styled-components';
import { UserCard } from './user-card';
import { TeamCard } from './team-card';
import Theme from '@odigos/ui-kit/theme';
import { PlatformCard } from './platform-card';
import { BUTTON_TEXTS } from '@odigos/ui-kit/constants';
import { useGenericForm, usePopup } from '@odigos/ui-kit/hooks';
import { RotateArrowsIcon, TrashIcon, UserIcon } from '@odigos/ui-kit/icons';
import { Drawer, FlexColumn, IconButton, Popup, Text } from '@odigos/ui-kit/components';

interface EditUserDrawerProps {
  user: User;
  onClose: () => void;
  onSave: () => void;
  onDelete: () => void;
  onResetPassword: () => void;
}

const Relative = styled.div`
  position: relative;
`;

const ActionItem = styled(Text)`
  display: flex;
  align-items: center;
  gap: 8px;
  text-wrap: nowrap;
  cursor: pointer;
  &:hover {
    text-decoration: underline;
  }
`;

const DRAWER_WIDTH = '750px';

const EditUserDrawer: FC<EditUserDrawerProps> = ({ user, onClose, onSave, onDelete, onResetPassword }) => {
  const theme = Theme.useTheme();
  const { popupRef, popupOpen, setPopupOpen } = usePopup();
  const { formData, handleFormChange, resetFormData, isFormDirty } = useGenericForm({ role: user.role, teams: user.teams, computePlatforms: user.computePlatforms });

  const onCancel = () => resetFormData();

  return (
    <Drawer
      width={DRAWER_WIDTH}
      isOpen
      onClose={onClose}
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
            onClick: onCancel,
            children: BUTTON_TEXTS.CANCEL,
          },
          {
            'data-id': 'drawer-save',
            variant: 'primary',
            onClick: onSave,
            children: BUTTON_TEXTS.SAVE,
          },
        ],
      }}
    >
      <FlexColumn $gap={24}>
        <UserCard formData={formData} handleFormChange={handleFormChange} />
        <PlatformCard formData={formData} handleFormChange={handleFormChange} />
        <TeamCard formData={formData} handleFormChange={handleFormChange} />
      </FlexColumn>
    </Drawer>
  );
};

export { EditUserDrawer, type EditUserDrawerProps };
