import React, { useState, type FC } from 'react';
import { PlatformCard } from './platform-card';
import { TeamsCard } from '../cards/teams-card';
import { MembersCard } from '../cards/members-card';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { BUTTON_TEXTS } from '@odigos/ui-kit/constants';
import { getPlatformIcon } from '@odigos/ui-kit/functions';
import type { ComputePlatform, ComputePlatformInput } from '@/types';
import { CancelWarning, Drawer, FlexColumn } from '@odigos/ui-kit/components';

interface EditComputePlatformDrawerProps {
  computePlatform: ComputePlatform;
  onClose: () => void;
  onSave: (payload: ComputePlatformInput) => void;
}

const DRAWER_WIDTH = '750px';

const EditComputePlatformDrawer: FC<EditComputePlatformDrawerProps> = ({ computePlatform, onClose, onSave }) => {
  const { formData, handleFormChange, resetFormData, isFormDirty } = useGenericForm({
    addUserIds: [],
    removeUserIds: [],
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
          icons: [getPlatformIcon(computePlatform.type)],
          title: computePlatform.name,
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
          <PlatformCard computePlatform={computePlatform} />
          <MembersCard currentUsers={computePlatform.users || []} formData={formData} handleFormChange={handleFormChange} />
          <TeamsCard currentTeams={computePlatform.teams || []} formData={formData} handleFormChange={handleFormChange} />
        </FlexColumn>
      </Drawer>

      <CancelWarning
        isOpen={cancelWarningOpen}
        name={computePlatform.name}
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

export { EditComputePlatformDrawer, type EditComputePlatformDrawerProps };
