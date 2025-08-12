import React, { useState } from 'react';
import { useTeamsCRUD } from '@/hooks';
import { StatusType } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { FORM_ALERTS } from '@odigos/ui-kit/constants';
import { ModalContent } from './create-team-modal.styled';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { DropdownOption, Modal } from '@odigos/ui-kit/components';
import { TeamCreatedSuccessfully } from './team-created-successfully';
import { CreateTeamForm, type CreateTeamFormData, CreateTeamFormField } from './create-team-form';

enum CreateTeamModalState {
  Form = 'form',
  Success = 'success',
}

interface CreateTeamModalProps {
  isOpen: boolean;
  onClose: () => void;
}

const INITIAL_FORM_DATA: CreateTeamFormData = {
  [CreateTeamFormField.Name]: '',
  [CreateTeamFormField.Members]: [],
  [CreateTeamFormField.ComputePlatforms]: [],
  [CreateTeamFormField.Description]: '',
};

export const CreateTeamModal: React.FC<CreateTeamModalProps> = ({ isOpen, onClose }) => {
  const { createTeam, creating } = useTeamsCRUD();
  const { addNotification } = useNotificationStore();
  const { formData, handleFormChange, formErrors, handleErrorChange, resetFormData } = useGenericForm(INITIAL_FORM_DATA);
  const [modalState, setModalState] = useState<CreateTeamModalState>(CreateTeamModalState.Form);

  const validateForm = (): boolean => {
    const newErrors: typeof formErrors = {};
    if (!formData[CreateTeamFormField.Name]) {
      newErrors[CreateTeamFormField.Name] = FORM_ALERTS.FIELD_IS_REQUIRED;
    }
    handleErrorChange(undefined, undefined, newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!validateForm()) return;
    try {
      const resData = await createTeam({
        name: formData[CreateTeamFormField.Name],
        description: formData[CreateTeamFormField.Description],
        users: formData[CreateTeamFormField.Members].map(({ id }) => id as string),
        computePlatforms: formData[CreateTeamFormField.ComputePlatforms].map(({ id }) => id as string),
      });

      if (resData?.success) {
        setModalState(CreateTeamModalState.Success);
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
    setModalState(CreateTeamModalState.Form);
    onClose();
  };

  const handleInputChange = (field: CreateTeamFormField, value: string | DropdownOption[]) => {
    handleFormChange(field, value);
    handleErrorChange(field, undefined);
  };

  return (
    <Modal isOpen={isOpen} onClose={handleClose} header={{ title: STRINGS.CREATE_TEAM_MODAL_TITLE }}>
      <ModalContent>
        {modalState === CreateTeamModalState.Form ? (
          <CreateTeamForm formData={formData} errors={formErrors} creating={creating} onChange={handleInputChange} onSubmit={handleSubmit} />
        ) : (
          <TeamCreatedSuccessfully name={formData[CreateTeamFormField.Name]} onDone={handleClose} />
        )}
      </ModalContent>
    </Modal>
  );
};
