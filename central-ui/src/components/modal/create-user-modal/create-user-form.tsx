import React, { useMemo } from 'react';
import { roleOptions } from './index';
import { useTeamsCRUD } from '@/hooks';
import { DropdownOption, Role } from '@/types';
import { StatusType } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { Input, Dropdown, NotificationNote, Text } from '@odigos/ui-kit/components';
import { Form, ButtonContainer, CreateButton, DescriptionTextWrapper, Row, Half } from './create-user-modal.styled';

export enum CreateUserFormField {
  Email = 'email',
  Role = 'role',
  Teams = 'teams',
  ComputePlatforms = 'computePlatforms',
}

export interface CreateUserFormData {
  [CreateUserFormField.Email]: string;
  [CreateUserFormField.Role]: DropdownOption<Role>;
  [CreateUserFormField.Teams]: DropdownOption[];
  [CreateUserFormField.ComputePlatforms]: DropdownOption[];
}

interface CreateUserFormProps {
  formData: CreateUserFormData;
  errors: Partial<Record<keyof CreateUserFormData, string>>;
  creating: boolean;
  onChange: (field: CreateUserFormField, value: string | DropdownOption<Role> | DropdownOption[]) => void;
  onSubmit: (e: React.FormEvent) => void;
}

export const CreateUserForm: React.FC<CreateUserFormProps> = ({ formData, errors, creating, onChange, onSubmit }) => {
  const { teams } = useTeamsCRUD();

  const teamOptions: DropdownOption[] = useMemo(() => teams.map((team) => ({ id: team.id, value: team.name })), [teams]);
  const platformOptions: DropdownOption[] = [];

  const isSubmitDisabled = !formData[CreateUserFormField.Email] || creating;

  return (
    <Form onSubmit={onSubmit}>
      <DescriptionTextWrapper>
        <Text size={14}>{STRINGS.CREATE_USER_FORM_DESCRIPTION_1}</Text>
        <Text size={14}>{STRINGS.CREATE_USER_FORM_DESCRIPTION_2}</Text>
      </DescriptionTextWrapper>
      <Row>
        <Half>
          <Input
            title={STRINGS.CREATE_USER_EMAIL_LABEL}
            required
            value={formData[CreateUserFormField.Email]}
            onChange={(e) => onChange(CreateUserFormField.Email, e.target.value)}
            placeholder={STRINGS.EMAIL_PLACEHOLDER}
            type='email'
          />
          {errors[CreateUserFormField.Email] && <NotificationNote type={StatusType.Error} title={STRINGS.ERROR_TITLE} message={errors[CreateUserFormField.Email]} />}
        </Half>
        <Half>
          <Dropdown
            title={STRINGS.CREATE_USER_ROLE_LABEL + ' *'}
            value={formData[CreateUserFormField.Role]}
            onSelect={(option) => onChange(CreateUserFormField.Role, option as DropdownOption<Role>)}
            options={roleOptions}
            placeholder={STRINGS.CREATE_USER_ROLE_LABEL}
            required
          />
        </Half>
      </Row>
      <Row>
        <Half>
          <Dropdown
            title={STRINGS.TEAMS_LABEL}
            value={formData[CreateUserFormField.Teams]}
            onSelect={(value) => onChange(CreateUserFormField.Teams, formData[CreateUserFormField.Teams].concat([value]))}
            onDeselect={(value) =>
              onChange(
                CreateUserFormField.Teams,
                formData[CreateUserFormField.Teams].filter(({ id }) => id !== value.id),
              )
            }
            options={teamOptions}
            placeholder={STRINGS.TEAMS_PLACEHOLDER}
            isMulti
          />
        </Half>
        <Half>
          <Dropdown
            title={STRINGS.COMPUTE_PLATFORM_LABEL}
            value={formData[CreateUserFormField.ComputePlatforms]}
            onSelect={(value) => onChange(CreateUserFormField.ComputePlatforms, formData[CreateUserFormField.ComputePlatforms].concat([value]))}
            onDeselect={(value) =>
              onChange(
                CreateUserFormField.ComputePlatforms,
                formData[CreateUserFormField.ComputePlatforms].filter(({ id }) => id !== value.id),
              )
            }
            options={platformOptions}
            placeholder={STRINGS.COMPUTE_PLATFORM_PLACEHOLDER}
            isMulti
          />
        </Half>
      </Row>
      <ButtonContainer>
        <CreateButton type='submit' disabled={isSubmitDisabled}>
          {creating ? STRINGS.CREATING : STRINGS.CREATE_USER_SUBMIT}
        </CreateButton>
      </ButtonContainer>
    </Form>
  );
};
