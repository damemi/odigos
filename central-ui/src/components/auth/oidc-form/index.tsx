import React, { type Dispatch, type SetStateAction, useMemo } from 'react';
import styled from 'styled-components';
import * as STRINGS from '@/utils/constants/strings';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { FlexColumn } from '@odigos/ui-kit/components';
import { VisualSuccess } from '@odigos/ui-kit/visuals';
import { AUTH_PROVIDERS } from '@/utils/constants/auth-providers';
import { Button, ButtonTab, Input, Note } from '@odigos/ui-kit/components/v2';
import { AuthProviderId, LoginType, AuthProviderInput, AuthProviderOption, EnsureIdentityProvider } from '@/types';
import { ConstrainedWidth, ErrorText, FormContainer, FormSubtitle, FormTitle } from '../styled';
import { StatusType } from '@odigos/ui-kit/types';

interface OidcFormProps {
  providerId: AuthProviderId;
  loading?: boolean;
  error?: string;
  onCreate: EnsureIdentityProvider;
  isCreateSuccess: boolean;
  setIsCreateSuccess: Dispatch<SetStateAction<boolean>>;
  onLogin: (providerId: AuthProviderId, loginType: LoginType) => void;
}

const VisualWrapper = styled.div`
  margin: 36px 0;
`;

const getFields = (provider: AuthProviderOption) => [
  {
    name: 'clientId' as keyof AuthProviderInput,
    label: STRINGS.CLIENT_ID,
    tooltip: STRINGS.CLIENT_ID_TOOLTIP,
    required: true,
  },
  {
    name: 'clientSecret' as keyof AuthProviderInput,
    type: 'password',
    label: STRINGS.CLIENT_SECRET,
    tooltip: STRINGS.CLIENT_SECRET_TOOLTIP,
    required: true,
  },
  {
    name: 'discoveryUrl' as keyof AuthProviderInput,
    label: STRINGS.DISCOVERY_URL,
    tooltip: STRINGS.DISCOVERY_URL_TOOLTIP,
    required: true,
  },
  {
    name: 'tenantId' as keyof AuthProviderInput,
    label: STRINGS.TENANT_ID,
    tooltip: STRINGS.TENANT_ID_TOOLTIP,
    required: provider.requireTenantId || false,
  },
];

const OidcForm: React.FC<OidcFormProps> = ({ providerId, loading, error, onCreate, isCreateSuccess, setIsCreateSuccess, onLogin }) => {
  const provider = useMemo(() => AUTH_PROVIDERS.filter((p) => p.loginType === LoginType.OIDC).find((p) => p.providerId === providerId), [providerId]);
  const fields = useMemo(() => (provider ? getFields(provider) : []), [provider]);

  const { formData, handleFormChange, formErrors, handleErrorChange } = useGenericForm<AuthProviderInput>({
    providerId,
    clientId: '',
    clientSecret: '',
    discoveryUrl: provider?.discoveryUrl || '',
    tenantId: '',
  });

  const validateForm = (values: typeof formData): boolean => {
    const errors: typeof formErrors = {};

    if (!values.clientId) {
      errors.clientId = STRINGS.CLIENT_ID_REQUIRED;
    }
    if (!values.clientSecret) {
      errors.clientSecret = STRINGS.CLIENT_SECRET_REQUIRED;
    }
    if (!values.discoveryUrl) {
      errors.discoveryUrl = STRINGS.DISCOVERY_URL_REQUIRED;
    }
    if (provider?.requireTenantId && !values.tenantId) {
      errors.tenantId = STRINGS.TENANT_ID_REQUIRED;
    }

    handleErrorChange(undefined, undefined, errors);
    const isValid = Object.keys(errors).length === 0;
    return isValid;
  };

  const handleSubmit = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();

    const isValid = validateForm(formData);
    if (!isValid) return;

    const res = await onCreate(formData);
    if (res?.success) {
      setIsCreateSuccess(true);
      // if we want to auto-redirect to the login page after creating the provider, we can do it here
      // onLogin(providerId);
    }
  };

  if (provider && isCreateSuccess) {
    return (
      <FormContainer $gap={59}>
        <FlexColumn $alignItems='center'>
          <VisualWrapper>
            <VisualSuccess />
          </VisualWrapper>

          <FlexColumn $gap={16}>
            <FormTitle>{STRINGS.PROVIDER_CREATED_SUCCESS}</FormTitle>
            <FormSubtitle>{STRINGS.PROVIDER_CREATED_SUCCESS_SUBTITLE(provider.displayName)}</FormSubtitle>
          </FlexColumn>
        </FlexColumn>

        <ConstrainedWidth>
          <FlexColumn $gap={8}>
            <ButtonTab label={`${STRINGS.LOGIN_WITH} ${provider.displayName}`} icon={provider.icon} loading={loading} onClick={() => onLogin(provider.providerId, LoginType.OIDC)} />
            <Note status={StatusType.Default} message={STRINGS.PROVIDER_REDIRECT_NOTICE(provider.displayName)} />
          </FlexColumn>
        </ConstrainedWidth>
      </FormContainer>
    );
  }

  return (
    <FormContainer $gap={41} onSubmit={handleSubmit}>
      <FlexColumn $gap={16}>
        <FormTitle>{STRINGS.CREATE_PROVIDER}</FormTitle>
        <FormSubtitle>{STRINGS.CREATE_PROVIDER_SUBTITLE}</FormSubtitle>
      </FlexColumn>

      <ConstrainedWidth>
        <FlexColumn $gap={12}>
          <Note status={StatusType.Default} message={STRINGS.SIGN_UP_ADMIN_DISCLAIMER} />
          {fields.map((field) => (
            <Input
              key={field.name}
              type={field.type}
              name={field.name}
              value={formData[field.name]}
              onChange={(e) => handleFormChange(e.target.name, e.target.value)}
              errorMessage={formErrors[field.name]}
              label={field.label}
              tooltip={field.tooltip}
              required={field.required}
            />
          ))}
        </FlexColumn>
      </ConstrainedWidth>

      <FlexColumn>
        {error && <ErrorText>{error}</ErrorText>}
        <Button type='submit' loading={loading} label={STRINGS.CREATE_PROVIDER} />
      </FlexColumn>
    </FormContainer>
  );
};

export { OidcForm, type OidcFormProps };
