import React, { useMemo } from 'react';
import { StatusType } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { useComputePlatformsCRUD, useUsersCRUD } from '@/hooks';
import { ButtonContainer, CreateButton, DescriptionTextWrapper, Form, Half, Row } from './create-team-modal.styled';
import { DropDown, type DropDataOption, Input, Note, TextArea, Typography, TypographySize } from '@odigos/ui-kit/components/v2';

export enum CreateTeamFormField {
  Name = 'name',
  Members = 'members',
  ComputePlatforms = 'platforms',
  Description = 'description',
}

export interface CreateTeamFormData {
  [CreateTeamFormField.Name]: string;
  [CreateTeamFormField.Members]: string[];
  [CreateTeamFormField.ComputePlatforms]: string[];
  [CreateTeamFormField.Description]: string;
}

interface CreateTeamFormProps {
  formData: CreateTeamFormData;
  errors: Partial<Record<keyof CreateTeamFormData, string>>;
  creating: boolean;
  onChange: (field: CreateTeamFormField, value: string | string[]) => void;
  onSubmit: (e: React.FormEvent) => void;
}

export const CreateTeamForm: React.FC<CreateTeamFormProps> = ({ formData, errors, creating, onChange, onSubmit }) => {
  const { users } = useUsersCRUD();
  const { computePlatforms } = useComputePlatformsCRUD();
  const membersOptions: DropDataOption[] = useMemo(() => users.map((user) => ({ id: user.id, label: user.email })), [users]);
  const platformsOptions: DropDataOption[] = useMemo(() => computePlatforms.map((platform) => ({ id: platform.id, label: platform.name })), [computePlatforms]);

  const isSubmitDisabled = !formData[CreateTeamFormField.Name] || creating;

  return (
    <Form onSubmit={onSubmit}>
      <DescriptionTextWrapper>
        <Typography size={TypographySize.S}>{STRINGS.CREATE_TEAM_FORM_DESCRIPTION_1}</Typography>
        <Typography size={TypographySize.S}>{STRINGS.CREATE_TEAM_FORM_DESCRIPTION_2}</Typography>
      </DescriptionTextWrapper>
      <Row>
        <Half>
          <Input
            label={STRINGS.TEAM_NAME}
            required
            value={formData[CreateTeamFormField.Name]}
            onChange={(e) => onChange(CreateTeamFormField.Name, e.target.value)}
            placeholder={STRINGS.TEAM_NAME_PLACEHOLDER}
          />
          {errors[CreateTeamFormField.Name] && <Note status={StatusType.Error} message={errors[CreateTeamFormField.Name] as string} />}
        </Half>
        <Half>
          <DropDown
            label={STRINGS.MEMBERS}
            required
            options={membersOptions}
            values={formData[CreateTeamFormField.Members]}
            setValues={(values) => onChange(CreateTeamFormField.Members, values)}
            placeholder={STRINGS.CHOOSE_MEMBER}
            withMultiSelect
            withSearch
          />
        </Half>
      </Row>
      <Row>
        <DropDown
          label={STRINGS.COMPUTE_PLATFORM}
          options={platformsOptions}
          values={formData[CreateTeamFormField.ComputePlatforms]}
          setValues={(values) => onChange(CreateTeamFormField.ComputePlatforms, values)}
          placeholder={STRINGS.CHOOSE_COMPUTE_PLATFORM}
          withMultiSelect
          withSearch
        />
      </Row>
      <Row>
        <TextArea
          label={STRINGS.DESCRIPTION}
          value={formData[CreateTeamFormField.Description]}
          onChange={(e) => onChange(CreateTeamFormField.Description, e.target.value)}
          placeholder={STRINGS.NOTES}
        />
      </Row>
      <ButtonContainer>
        <CreateButton type='submit' disabled={isSubmitDisabled} loading={creating} label={creating ? STRINGS.CREATING : STRINGS.CREATE_TEAM} />
      </ButtonContainer>
    </Form>
  );
};
