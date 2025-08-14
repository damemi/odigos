import React, { useState } from 'react';
import { ROLE_OPTIONS } from '@/utils';
import { useUsersCRUD } from '@/hooks';
import { Role } from '@/types';
import { StatusType } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { ModalContent } from './create-user-modal.styled';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { DropdownOption, Modal } from '@odigos/ui-kit/components';
import { UserCreatedSuccessfully } from './user-created-successfully';
import { CreateUserForm, CreateUserFormData, CreateUserFormField } from './create-user-form';

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
  [CreateUserFormField.Role]: ROLE_OPTIONS.find((opt) => opt.id === Role.VIEWER)!,
  [CreateUserFormField.Teams]: [],
  [CreateUserFormField.ComputePlatforms]: [],
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
      newErrors[CreateUserFormField.Email] = STRINGS.CREATE_USER_EMAIL_REQUIRED;
    } else if (!/\S+@\S+\.\S+/.test(formData[CreateUserFormField.Email])) {
      newErrors[CreateUserFormField.Email] = STRINGS.CREATE_USER_EMAIL_INVALID;
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
        role: formData[CreateUserFormField.Role].id as Role,
        password: undefined,
        addTeamIds: formData[CreateUserFormField.Teams].map(({ id }) => id as string),
        removeTeamIds: [],
        addComputePlatformIds: formData[CreateUserFormField.ComputePlatforms].map(({ id }) => id as string),
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

  const handleInputChange = (field: CreateUserFormField, value: string | DropdownOption<Role> | DropdownOption[]) => {
    handleFormChange(field, value);
    handleErrorChange(field, undefined);
  };

  return (
    <Modal isOpen={isOpen} onClose={handleClose} header={{ title: STRINGS.CREATE_USER_MODAL_TITLE }}>
      <ModalContent>
        {modalState === CreateUserModalState.Form ? (
          <CreateUserForm formData={formData} errors={formErrors} creating={creating} onChange={handleInputChange} onSubmit={handleSubmit} />
        ) : (
          <UserCreatedSuccessfully email={formData[CreateUserFormField.Email]} password={generatedPassword ?? ''} onDone={handleClose} />
        )}
      </ModalContent>
    </Modal>
  );
};
