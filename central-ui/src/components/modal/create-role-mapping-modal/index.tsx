import React from 'react';
import { ROLE_OPTIONS } from '@/utils';
import { useRoleMappingsCRUD } from '@/hooks';
import { StatusType } from '@odigos/ui-kit/types';
import { AuthProviderId, Role } from '@/types';
import * as STRINGS from '@/utils/constants/strings';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { Actions, Body, Container, DescriptionTextWrapper, Form, Half, Header, Row } from './create-role-mapping-modal.styled';
import { Button, ButtonVariants, DropDown, Input, Modal, Note, Typography, TypographySize, TypographyVariants } from '@odigos/ui-kit/components';

interface CreateRoleMappingModalProps {
  isOpen: boolean;
  onClose: () => void;
}

enum FormField {
  ClaimValue = 'claimValue',
  ClaimName = 'claimName',
  Role = 'role',
  DisplayName = 'displayName',
}

interface FormData {
  [FormField.ClaimValue]: string;
  [FormField.ClaimName]: string;
  [FormField.Role]: Role;
  [FormField.DisplayName]: string;
}

const INITIAL_FORM_DATA: FormData = {
  [FormField.ClaimValue]: '',
  [FormField.ClaimName]: STRINGS.CLAIM_NAME_PLACEHOLDER,
  [FormField.Role]: Role.VIEWER,
  [FormField.DisplayName]: '',
};

export const CreateRoleMappingModal: React.FC<CreateRoleMappingModalProps> = ({ isOpen, onClose }) => {
  const { createRoleMapping, creating } = useRoleMappingsCRUD();
  const { addNotification } = useNotificationStore();
  const { formData, handleFormChange, formErrors, handleErrorChange, resetFormData } = useGenericForm(INITIAL_FORM_DATA);

  const validateForm = (): boolean => {
    const newErrors: typeof formErrors = {};
    if (!formData[FormField.ClaimValue]) {
      newErrors[FormField.ClaimValue] = STRINGS.CLAIM_VALUE_REQUIRED;
    }
    handleErrorChange(undefined, undefined, newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const handleClose = () => {
    resetFormData();
    handleErrorChange(undefined, undefined, {});
    onClose();
  };

  const handleInputChange = (field: FormField, value: string) => {
    handleFormChange(field, value);
    handleErrorChange(field, undefined);
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!validateForm()) return;
    try {
      const result = await createRoleMapping({
        providerId: AuthProviderId.AZURE,
        claimValue: formData[FormField.ClaimValue],
        claimName: formData[FormField.ClaimName] || undefined,
        role: formData[FormField.Role],
        displayName: formData[FormField.DisplayName] || undefined,
      });

      if (result?.success) {
        handleClose();
      } else if (result?.error) {
        addNotification({ type: StatusType.Error, message: result.error, hideFromHistory: false });
      }
    } catch {
      addNotification({ type: StatusType.Error, message: STRINGS.CREATE_ROLE_MAPPING_ERROR, hideFromHistory: false });
    }
  };

  const isSubmitDisabled = !formData[FormField.ClaimValue] || creating;

  return (
    <Modal isOpen={isOpen} onClose={handleClose}>
      <Container>
        <Header>
          <Typography variant={TypographyVariants.H2} size={TypographySize.L}>
            {STRINGS.CREATE_ROLE_MAPPING}
          </Typography>
          <DescriptionTextWrapper>
            <Typography size={TypographySize.S}>{STRINGS.CREATE_ROLE_MAPPING_FORM_DESCRIPTION_1}</Typography>
            <Typography size={TypographySize.S}>{STRINGS.CREATE_ROLE_MAPPING_FORM_DESCRIPTION_2}</Typography>
          </DescriptionTextWrapper>
        </Header>
        <Form onSubmit={handleSubmit}>
          <Body>
            <Row>
              <Half>
                <Input
                  label={STRINGS.CLAIM_VALUE}
                  required
                  value={formData[FormField.ClaimValue]}
                  onChange={(e) => handleInputChange(FormField.ClaimValue, e.target.value)}
                  placeholder={STRINGS.CLAIM_VALUE_PLACEHOLDER}
                  tooltip={STRINGS.CLAIM_VALUE_TOOLTIP}
                />
                {formErrors[FormField.ClaimValue] && <Note status={StatusType.Error} message={formErrors[FormField.ClaimValue] as string} />}
              </Half>
              <Half>
                <DropDown
                  label={STRINGS.ROLE}
                  required
                  options={ROLE_OPTIONS}
                  values={[formData[FormField.Role]]}
                  setValues={([value]) => handleInputChange(FormField.Role, (value as Role) ?? Role.VIEWER)}
                  placeholder={STRINGS.ROLE}
                />
              </Half>
            </Row>
            <Row>
              <Half>
                <Input
                  label={STRINGS.CLAIM_NAME}
                  value={formData[FormField.ClaimName]}
                  onChange={(e) => handleInputChange(FormField.ClaimName, e.target.value)}
                  placeholder={STRINGS.CLAIM_NAME_PLACEHOLDER}
                  tooltip={STRINGS.CLAIM_NAME_TOOLTIP}
                />
              </Half>
              <Half>
                <Input
                  label={STRINGS.DISPLAY_NAME}
                  value={formData[FormField.DisplayName]}
                  onChange={(e) => handleInputChange(FormField.DisplayName, e.target.value)}
                  placeholder={STRINGS.DISPLAY_NAME_PLACEHOLDER}
                  tooltip={STRINGS.DISPLAY_NAME_TOOLTIP}
                />
              </Half>
            </Row>
          </Body>
          <Actions>
            <Button type='button' variant={ButtonVariants.Secondary} label={STRINGS.CANCEL} onClick={handleClose} />
            <Button type='submit' variant={ButtonVariants.Primary} disabled={isSubmitDisabled} loading={creating} label={creating ? STRINGS.CREATING : STRINGS.CREATE_ROLE_MAPPING} />
          </Actions>
        </Form>
      </Container>
    </Modal>
  );
};
