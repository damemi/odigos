import React, { useMemo } from 'react';
import { useUsersCRUD } from '@/hooks';
import { DropdownOption } from '@/types';
import { StatusType } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { Input, Dropdown, NotificationNote, Text, TextArea } from '@odigos/ui-kit/components';
import { Form, ButtonContainer, CreateButton, DescriptionTextWrapper, Row, Half } from './create-team-modal.styled';

export enum CreateTeamFormField {
  Name = 'name',
  Members = 'members',
  ComputePlatforms = 'platforms',
  Description = 'description',
}

export interface CreateTeamFormData {
  [CreateTeamFormField.Name]: string;
  [CreateTeamFormField.Members]: DropdownOption[];
  [CreateTeamFormField.ComputePlatforms]: DropdownOption[];
  [CreateTeamFormField.Description]: string;
}

interface CreateTeamFormProps {
  formData: CreateTeamFormData;
  errors: Partial<Record<keyof CreateTeamFormData, string>>;
  creating: boolean;
  onChange: (field: CreateTeamFormField, value: string | DropdownOption[]) => void;
  onSubmit: (e: React.FormEvent) => void;
}

export const CreateTeamForm: React.FC<CreateTeamFormProps> = ({ formData, errors, creating, onChange, onSubmit }) => {
  const { users } = useUsersCRUD();

  const membersOptions: DropdownOption[] = useMemo(() => users.map((user) => ({ id: user.id, value: user.email })), [users]);
  const platformsOptions: DropdownOption[] = [];

  const isSubmitDisabled = !formData[CreateTeamFormField.Name] || creating;

  return (
    <Form onSubmit={onSubmit}>
      <DescriptionTextWrapper>
        <Text size={14}>{STRINGS.CREATE_TEAM_FORM_DESCRIPTION_1}</Text>
        <Text size={14}>{STRINGS.CREATE_TEAM_FORM_DESCRIPTION_2}</Text>
      </DescriptionTextWrapper>
      <Row>
        <Half>
          <Input
            title={STRINGS.TEAM_NAME}
            required
            value={formData[CreateTeamFormField.Name]}
            onChange={(e) => onChange(CreateTeamFormField.Name, e.target.value)}
            placeholder={STRINGS.TEAM_NAME_PLACEHOLDER}
          />
          {errors[CreateTeamFormField.Name] && <NotificationNote type={StatusType.Error} title={STRINGS.ERROR_TITLE} message={errors[CreateTeamFormField.Name]} />}
        </Half>
        <Half>
          <Dropdown
            title={STRINGS.MEMBERS_LABEL}
            required
            value={formData[CreateTeamFormField.Members]}
            onSelect={(value) => onChange(CreateTeamFormField.Members, formData[CreateTeamFormField.Members].concat([value]))}
            onDeselect={(value) =>
              onChange(
                CreateTeamFormField.Members,
                formData[CreateTeamFormField.Members].filter(({ id }) => id !== value.id),
              )
            }
            options={membersOptions}
            placeholder={STRINGS.MEMBERS_PLACEHOLDER}
            isMulti
          />
        </Half>
      </Row>
      <Row>
        <Dropdown
          title={STRINGS.COMPUTE_PLATFORM_LABEL}
          value={formData[CreateTeamFormField.ComputePlatforms]}
          onSelect={(value) => onChange(CreateTeamFormField.ComputePlatforms, formData[CreateTeamFormField.ComputePlatforms].concat([value]))}
          onDeselect={(value) =>
            onChange(
              CreateTeamFormField.ComputePlatforms,
              formData[CreateTeamFormField.ComputePlatforms].filter(({ id }) => id !== value.id),
            )
          }
          options={platformsOptions}
          placeholder={STRINGS.COMPUTE_PLATFORM_PLACEHOLDER}
          isMulti
        />
      </Row>
      <Row>
        <TextArea
          title={STRINGS.DESCRIPTION}
          value={formData[CreateTeamFormField.Description]}
          onChange={(e) => onChange(CreateTeamFormField.Description, e.target.value)}
          placeholder={STRINGS.CREATE_TEAM_FORM_DESCRIPTION_PLACEHOLDER}
        />
      </Row>
      <ButtonContainer>
        <CreateButton type='submit' disabled={isSubmitDisabled}>
          {creating ? STRINGS.CREATING : STRINGS.CREATE_TEAM_SUBMIT}
        </CreateButton>
      </ButtonContainer>
    </Form>
  );
};
