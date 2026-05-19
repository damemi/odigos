import React from 'react';
import { Role } from '@/types';
import { ROLE_OPTIONS } from '@/utils';
import { StatusType } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { DropDown, Input, Note, Typography, TypographySize } from '@odigos/ui-kit/components/v2';
import { ButtonContainer, CreateButton, DescriptionTextWrapper, Form, Half, Row } from './create-user-modal.styled';

export enum CreateUserFormField {
  Email = 'email',
  Role = 'role',
  Teams = 'teams',
  ComputePlatforms = 'computePlatforms',
}

export interface CreateUserFormData {
  [CreateUserFormField.Email]: string;
  [CreateUserFormField.Role]: Role;
  // [CreateUserFormField.Teams]: string[];
  // [CreateUserFormField.ComputePlatforms]: string[];
}

interface CreateUserFormProps {
  formData: CreateUserFormData;
  errors: Partial<Record<keyof CreateUserFormData, string>>;
  creating: boolean;
  onChange: (field: CreateUserFormField, value: string) => void;
  onSubmit: (e: React.FormEvent) => void;
}

export const CreateUserForm: React.FC<CreateUserFormProps> = ({ formData, errors, creating, onChange, onSubmit }) => {
  const isSubmitDisabled = !formData[CreateUserFormField.Email] || creating;

  return (
    <Form onSubmit={onSubmit}>
      <DescriptionTextWrapper>
        <Typography size={TypographySize.S}>{STRINGS.CREATE_USER_FORM_DESCRIPTION_1}</Typography>
        <Typography size={TypographySize.S}>{STRINGS.CREATE_USER_FORM_DESCRIPTION_2}</Typography>
      </DescriptionTextWrapper>
      <Row>
        <Half>
          <Input
            label={STRINGS.EMAIL}
            required
            value={formData[CreateUserFormField.Email]}
            onChange={(e) => onChange(CreateUserFormField.Email, e.target.value)}
            placeholder={STRINGS.EMAIL_PLACEHOLDER}
            type='email'
          />
          {errors[CreateUserFormField.Email] && <Note status={StatusType.Error} message={errors[CreateUserFormField.Email] as string} />}
        </Half>
        <Half>
          <DropDown
            label={STRINGS.ROLE}
            required
            options={ROLE_OPTIONS}
            values={[formData[CreateUserFormField.Role]]}
            setValues={([value]) => onChange(CreateUserFormField.Role, value ?? Role.VIEWER)}
            placeholder={STRINGS.ROLE}
          />
        </Half>
      </Row>
      <ButtonContainer>
        <CreateButton type='submit' disabled={isSubmitDisabled} loading={creating} label={creating ? STRINGS.CREATING : STRINGS.CREATE_USER} />
      </ButtonContainer>
    </Form>
  );
};
