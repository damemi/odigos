import React, { Fragment, useMemo, useState } from 'react';
import { type HandleSignIn } from '@/hooks';
import { StatusType } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { FlexColumn } from '@odigos/ui-kit/components';
import { Button, ButtonSize, ButtonVariants, Input, Note } from '@odigos/ui-kit/components/v2';
import { ConstrainedWidth, ErrorText, FormContainer, FormSubtitle, FormTitle } from '../styled';

enum FormMode {
  Login = 'login',
  Register = 'register',
}

interface AuthFormProps {
  isFirstTime: boolean;
  loading: boolean;
  error: string | null;
  onSubmit: HandleSignIn;
}

const getFields = (formMode: FormMode) => {
  const arr = [
    {
      name: 'email',
      type: 'email',
      label: STRINGS.EMAIL,
      required: true,
    },
    {
      name: 'password',
      type: 'password',
      label: STRINGS.PASSWORD,
      required: true,
    },
  ];

  if (formMode === FormMode.Register) {
    arr.push({
      name: 'confirmPassword',
      type: 'password',
      label: STRINGS.CONFIRM_PASSWORD,
      required: true,
    });
  }

  return arr;
};

const AuthForm: React.FC<AuthFormProps> = ({ isFirstTime, loading, error, onSubmit }) => {
  const [formMode, setFormMode] = useState<FormMode>(isFirstTime ? FormMode.Register : FormMode.Login);
  const fields = useMemo(() => getFields(formMode), [formMode]);

  const { formData, handleFormChange, formErrors, handleErrorChange } = useGenericForm({
    email: '',
    password: '',
    confirmPassword: '',
  });

  const validateForm = (values: typeof formData): boolean => {
    const errors: typeof formErrors = {};

    if (!values.email) {
      errors.email = STRINGS.EMAIL_REQUIRED;
    }
    if (!values.password) {
      errors.password = STRINGS.PASSWORD_REQUIRED;
    }
    if (formMode === FormMode.Register && values.confirmPassword !== values.password) {
      errors.confirmPassword = STRINGS.PASSWORDS_DO_NOT_MATCH;
    }

    handleErrorChange(undefined, undefined, errors);
    const isValid = Object.keys(errors).length === 0;
    return isValid;
  };

  const handleSubmit = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();

    const isValid = validateForm(formData);
    if (!isValid) return;

    await onSubmit(formData);
  };

  return (
    <FormContainer $gap={41} onSubmit={handleSubmit}>
      <FlexColumn $gap={16}>
        <FormTitle>{formMode === FormMode.Register ? STRINGS.SIGN_UP_ADMIN_TITLE : STRINGS.SIGN_IN}</FormTitle>
        <FormSubtitle>{formMode === FormMode.Register ? STRINGS.SIGN_UP_ADMIN_SUBTITLE_2 : STRINGS.SIGN_IN_SUBTITLE_2}</FormSubtitle>
      </FlexColumn>

      <ConstrainedWidth>
        <FlexColumn $gap={12}>
          {fields.map((field) => (
            <Fragment key={field.name}>
              <Input
                type={field.type}
                name={field.name}
                value={formData[field.name as keyof typeof formData]}
                onChange={(e) => handleFormChange(e.target.name, e.target.value)}
                errorMessage={formErrors[field.name as keyof typeof formErrors]}
                label={field.label}
                required={field.required}
              />
              {formMode === FormMode.Register && field.name === 'email' && <Note status={StatusType.Default} message={STRINGS.SIGN_UP_ADMIN_DISCLAIMER} />}
            </Fragment>
          ))}
        </FlexColumn>
      </ConstrainedWidth>

      <FlexColumn>
        {error && <ErrorText>{error}</ErrorText>}
        <Button type='submit' fullWidth loading={loading} label={formMode === FormMode.Register ? STRINGS.SIGN_UP : STRINGS.SIGN_IN} />
        {formMode === FormMode.Register && <Button type='button' variant={ButtonVariants.Text} size={ButtonSize.S} label='Already have an account?' onClick={() => setFormMode(FormMode.Login)} />}
      </FlexColumn>
    </FormContainer>
  );
};

export { AuthForm, type AuthFormProps };
