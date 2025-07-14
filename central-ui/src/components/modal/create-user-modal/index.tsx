import React, { useState } from 'react';
import { useUsersCRUD } from '@/hooks';
import { DropdownOption, Role } from '@/types';
import { StatusType } from '@odigos/ui-kit/types';
import { Modal } from '@odigos/ui-kit/components';
import * as STRINGS from '@/utils/constants/strings';
import { ModalContent } from './create-user-modal.styled';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { UserCreatedSuccessfully } from './user-created-successfully';
import { CreateUserForm, CreateUserFormField } from './create-user-form';

enum CreateUserModalState {
  Form = 'form',
  Success = 'success',
}

export const roleOptions: DropdownOption<Role>[] = [
  { id: Role.OWNER, value: 'OWNER' },
  { id: Role.ADMIN, value: 'ADMIN' },
  { id: Role.MAINTAINER, value: 'MAINTAINER' },
  { id: Role.VIEWER, value: 'VIEWER' },
];

interface CreateUserModalProps {
  isOpen: boolean;
  onClose: () => void;
}

interface CreateUserFormData {
  [CreateUserFormField.Email]: string;
  [CreateUserFormField.Role]: DropdownOption<Role>;
  [CreateUserFormField.Teams]: DropdownOption[];
  [CreateUserFormField.ComputePlatforms]: DropdownOption[];
}

export const CreateUserModal: React.FC<CreateUserModalProps> = ({ isOpen, onClose }) => {
  const { createUser, creating } = useUsersCRUD();
  const { addNotification } = useNotificationStore();
  const [formData, setFormData] = useState<CreateUserFormData>({
    [CreateUserFormField.Email]: '',
    [CreateUserFormField.Role]: roleOptions.find((opt) => opt.id === Role.VIEWER)!,
    [CreateUserFormField.Teams]: [],
    [CreateUserFormField.ComputePlatforms]: [],
  });
  const [errors, setErrors] = useState<Partial<CreateUserFormData>>({});
  const [modalState, setModalState] = useState<CreateUserModalState>(CreateUserModalState.Form);
  const [generatedPassword, setGeneratedPassword] = useState<string | null>(null);

  const validateForm = (): boolean => {
    const newErrors: Partial<CreateUserFormData> = {};
    if (!formData[CreateUserFormField.Email]) {
      newErrors[CreateUserFormField.Email] = STRINGS.CREATE_USER_EMAIL_REQUIRED;
    } else if (!/\S+@\S+\.\S+/.test(formData[CreateUserFormField.Email])) {
      newErrors[CreateUserFormField.Email] = STRINGS.CREATE_USER_EMAIL_INVALID;
    }
    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!validateForm()) return;
    try {
      const resp = await createUser({
        email: formData[CreateUserFormField.Email],
        role: formData[CreateUserFormField.Role].id as Role,
        password: undefined,
      });
      const password = resp?.data?.createUser?.generatedPassword;
      if (password) {
        setGeneratedPassword(password);
        setModalState(CreateUserModalState.Success);
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
    setFormData({
      [CreateUserFormField.Email]: '',
      [CreateUserFormField.Role]: roleOptions.find((opt) => opt.id === Role.VIEWER)!,
      [CreateUserFormField.Teams]: [],
      [CreateUserFormField.ComputePlatforms]: [],
    });
    setErrors({});
    setModalState(CreateUserModalState.Form);
    setGeneratedPassword(null);
    onClose();
  };

  const handleInputChange = (field: CreateUserFormField, value: string | DropdownOption<Role> | DropdownOption[]) => {
    setFormData((prev) => ({ ...prev, [field]: value }));
    if (errors[field]) {
      setErrors((prev) => ({ ...prev, [field]: undefined }));
    }
  };

  return (
    <Modal isOpen={isOpen} onClose={handleClose} header={{ title: STRINGS.CREATE_USER_MODAL_TITLE }}>
      <ModalContent>
        {modalState === CreateUserModalState.Form ? (
          <CreateUserForm formData={formData} errors={errors} creating={creating} onChange={handleInputChange} onSubmit={handleSubmit} />
        ) : (
          <UserCreatedSuccessfully email={formData[CreateUserFormField.Email]} password={generatedPassword ?? ''} onDone={handleClose} />
        )}
      </ModalContent>
    </Modal>
  );
};
