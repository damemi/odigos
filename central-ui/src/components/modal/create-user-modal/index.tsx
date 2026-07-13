import React, { useState } from 'react';
import { Role } from '@/types';
import { useUsersCRUD } from '@/hooks';
import { StatusType } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { ModalContent } from './create-user-modal.styled';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { UserCreatedSuccessfully } from './user-created-successfully';
import { CreateUserForm, CreateUserFormData, CreateUserFormField } from './create-user-form';
import { Modal, Typography, TypographySize, TypographyVariants } from '@odigos/ui-kit/components';

enum CreateUserModalState {
  Form = 'form',
  Success = 'success',
}

interface CreateUserModalProps {
  isOpen: boolean;
  onClose: () => void;
}

const INITIAL_FORM_DATA: CreateUserFormData = {
  [CreateUserFormField.Email]: '',
  [CreateUserFormField.Role]: Role.VIEWER,
};

export const CreateUserModal: React.FC<CreateUserModalProps> = ({ isOpen, onClose }) => {
  const { createUser, creating } = useUsersCRUD();
  const { addNotification } = useNotificationStore();
  const { formData, handleFormChange, formErrors, handleErrorChange, resetFormData } = useGenericForm(INITIAL_FORM_DATA);
  const [modalState, setModalState] = useState<CreateUserModalState>(CreateUserModalState.Form);
  const [generatedPassword, setGeneratedPassword] = useState<string | null>(null);

  const validateForm = (): boolean => {
    const newErrors: typeof formErrors = {};
    if (!formData[CreateUserFormField.Email]) {
      newErrors[CreateUserFormField.Email] = STRINGS.EMAIL_REQUIRED;
    } else if (!/\S+@\S+\.\S+/.test(formData[CreateUserFormField.Email])) {
      newErrors[CreateUserFormField.Email] = STRINGS.EMAIL_INVALID;
    }
    handleErrorChange(undefined, undefined, newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!validateForm()) return;
    try {
      const resData = await createUser({
        email: formData[CreateUserFormField.Email],
        role: formData[CreateUserFormField.Role],
        password: undefined,
        addTeamIds: [],
        removeTeamIds: [],
        addComputePlatformIds: [],
        removeComputePlatformIds: [],
      });

      if (resData?.success) {
        const { generatedPassword } = resData;
        if (generatedPassword) {
          setGeneratedPassword(generatedPassword);
          setModalState(CreateUserModalState.Success);
        }
      }
    } catch {
      addNotification({
        type: StatusType.Error,
        message: STRINGS.CREATE_USER_ERROR,
        hideFromHistory: false,
      });
    }
  };

  const handleClose = () => {
    resetFormData();
    handleErrorChange(undefined, undefined, {});
    setModalState(CreateUserModalState.Form);
    setGeneratedPassword(null);
    onClose();
  };

  const handleInputChange = (field: CreateUserFormField, value: string) => {
    handleFormChange(field, value);
    handleErrorChange(field, undefined);
  };

  return (
    <Modal isOpen={isOpen} onClose={handleClose}>
      <ModalContent>
        <Typography variant={TypographyVariants.H2} size={TypographySize.L}>
          {STRINGS.CREATE_USER}
        </Typography>
        {modalState === CreateUserModalState.Form ? (
          <CreateUserForm formData={formData} errors={formErrors} creating={creating} onChange={handleInputChange} onSubmit={handleSubmit} />
        ) : (
          <UserCreatedSuccessfully email={formData[CreateUserFormField.Email]} password={generatedPassword ?? ''} onDone={handleClose} />
        )}
      </ModalContent>
    </Modal>
  );
};
