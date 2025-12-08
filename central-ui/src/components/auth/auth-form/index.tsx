import React, { Fragment, useMemo } from 'react';
import { type HandleSignIn } from '@/hooks';
import { StatusType } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { FlexColumn } from '@odigos/ui-kit/components';
import { Button, Input, Note } from '@odigos/ui-kit/components/v2';
import { ConstrainedWidth, ErrorText, FormContainer, FormSubtitle, FormTitle } from '../styled';

interface AuthFormProps {
  isFirstTime: boolean;
  loading: boolean;
  error: string | null;
  onSubmit: HandleSignIn;
}

const getFields = (isFirstTime: AuthFormProps['isFirstTime']) => {
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

  if (isFirstTime) {
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
  const fields = useMemo(() => getFields(isFirstTime), [isFirstTime]);

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
    if (isFirstTime && values.confirmPassword !== values.password) {
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
        <FormTitle>{isFirstTime ? STRINGS.SIGN_UP_ADMIN_TITLE : STRINGS.SIGN_IN}</FormTitle>
        <FormSubtitle>{isFirstTime ? STRINGS.SIGN_UP_ADMIN_SUBTITLE_2 : STRINGS.SIGN_IN_SUBTITLE_2}</FormSubtitle>
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
              {isFirstTime && field.name === 'email' && <Note status={StatusType.Default} message={STRINGS.SIGN_UP_ADMIN_DISCLAIMER} />}
            </Fragment>
          ))}
        </FlexColumn>
      </ConstrainedWidth>

      <FlexColumn>
        {error && <ErrorText>{error}</ErrorText>}
        <Button type='submit' loading={loading} label={isFirstTime ? STRINGS.SIGN_UP : STRINGS.SIGN_IN} />
      </FlexColumn>
    </FormContainer>
  );
};

export { AuthForm, type AuthFormProps };
