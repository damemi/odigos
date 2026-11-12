import React from 'react';
import * as STRINGS from '@/utils/constants/strings';
import { ModalContent } from '../create-user-modal/create-user-modal.styled';
import { UserCreatedSuccessfully } from '../create-user-modal/user-created-successfully';
import { Modal, Typography, TypographySize, TypographyVariants } from '@odigos/ui-kit/components';

interface ResetPasswordModalProps {
  email: string;
  password: string;
  onClose: () => void;
}

export const ResetPasswordModal: React.FC<ResetPasswordModalProps> = ({ email, password, onClose }) => {
  return (
    <Modal isOpen onClose={onClose}>
      <ModalContent>
        <Typography variant={TypographyVariants.H2} size={TypographySize.L}>
          {STRINGS.RESET_PASSWORD}
        </Typography>
        <UserCreatedSuccessfully
          title={STRINGS.PASSWORD_RESET_SUCCESS}
          subtitle={STRINGS.PASSWORD_RESET_SUCCESS_SUBTITLE(email)}
          disclaimer={STRINGS.RESET_PASSWORD_DISCLAIMER}
          password={password}
          onDone={onClose}
        />
      </ModalContent>
    </Modal>
  );
};
