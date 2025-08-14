import React, { useState, type FC } from 'react';
import type { Team, TeamInput } from '@/types';
import styled from 'styled-components';
import { TeamCard } from './team-card';
import Theme from '@odigos/ui-kit/theme';
import { Relative } from '@/components/styled';
import { MembersCard } from '../cards/members-card';
import { BUTTON_TEXTS } from '@odigos/ui-kit/constants';
import { PlatformsCard } from '../cards/platforms-card';
import { useGenericForm, usePopup } from '@odigos/ui-kit/hooks';
import { TrashIcon, UserGroupIcon } from '@odigos/ui-kit/icons';
import { CancelWarning, Drawer, FlexColumn, IconButton, Popup, Text } from '@odigos/ui-kit/components';

interface EditTeamDrawerProps {
  team: Team;
  onClose: () => void;
  onSave: (payload: TeamInput) => void;
  onDelete: () => void;
}

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

const EditTeamDrawer: FC<EditTeamDrawerProps> = ({ team, onClose, onSave, onDelete }) => {
  const theme = Theme.useTheme();
  const { popupRef, popupOpen, setPopupOpen } = usePopup();
  const { formData, handleFormChange, resetFormData, isFormDirty } = useGenericForm({
    name: team.name,
    description: team.description || '',
    addUserIds: [],
    removeUserIds: [],
    addComputePlatformIds: [],
    removeComputePlatformIds: [],
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
          icons: [UserGroupIcon],
          title: team.name,
          actions: [
            <Relative key='actions'>
              <IconButton withBorder onClick={() => setPopupOpen((prev) => !prev)}>
                <Text size={10}>•••</Text>
              </IconButton>
              <Popup ref={popupRef} isOpen={popupOpen} top={36} right={0} padding='24px'>
                <FlexColumn $gap={12}>
                  <ActionItem size={14} color={theme.text.error} onClick={onDelete}>
                    <TrashIcon /> Delete Team
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
          <TeamCard team={team} formData={formData} handleFormChange={handleFormChange} />
          <PlatformsCard currentComputePlatforms={team.computePlatforms || []} formData={formData} handleFormChange={handleFormChange} />
          <MembersCard currentUsers={team.users || []} formData={formData} handleFormChange={handleFormChange} />
        </FlexColumn>
      </Drawer>

      <CancelWarning
        isOpen={cancelWarningOpen}
        name={team.name}
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

export { EditTeamDrawer, type EditTeamDrawerProps };
