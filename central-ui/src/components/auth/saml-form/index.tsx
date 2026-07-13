import React, { useState, useMemo, type Dispatch, type SetStateAction } from 'react';
import styled from 'styled-components';
import { StatusType } from '@odigos/ui-kit/types';
import { VSquareIcon } from '@odigos/ui-kit/icons';
import * as STRINGS from '@/utils/constants/strings';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { FlexColumn, Tooltip } from '@odigos/ui-kit/components';
import { VisualGreenRings } from '@odigos/ui-kit/visuals';
import { AUTH_PROVIDERS } from '@/utils/constants/auth-providers';
import { Button, ButtonTab, CliCommand, Input, Note } from '@odigos/ui-kit/components';
import { ConstrainedWidth, ErrorText, FormContainer, FormSubtitle, FormTitle } from '../styled';
import { AuthProviderId, LoginType, type SamlIdentityProviderInput, EnsureSamlIdentityProvider } from '@/types';

interface SamlFormProps {
  providerId: AuthProviderId;
  loading?: boolean;
  error?: string;
  onCreate: EnsureSamlIdentityProvider;
  isCreateSuccess: boolean;
  setIsCreateSuccess: Dispatch<SetStateAction<boolean>>;
  onLogin: (providerId: AuthProviderId, loginType: LoginType) => void;
}

const VisualWrapper = styled.div`
  margin: 36px 0;
`;

const FullWidthTooltip = styled.div`
  width: 100%;
  & > div {
    width: 100%;
  }
`;

const SamlForm: React.FC<SamlFormProps> = ({ providerId, loading, error, onCreate, isCreateSuccess, setIsCreateSuccess, onLogin }) => {
  const [callbackUrl, setCallbackUrl] = useState<string | undefined>();
  const [hasCopiedCallbackUrl, setHasCopiedCallbackUrl] = useState(false);
  const { formData, handleFormChange, formErrors, handleErrorChange } = useGenericForm<SamlIdentityProviderInput>({
    providerId,
    idpEntityId: '',
    singleSignOnServiceUrl: '',
    signingCertificate: '',
  });
  const provider = useMemo(() => AUTH_PROVIDERS.filter((p) => p.loginType === LoginType.SAML).find((p) => p.providerId === providerId), [providerId]);

  const validateForm = (values: typeof formData): boolean => {
    const errors: typeof formErrors = {};

    if (!values.idpEntityId) {
      errors.idpEntityId = STRINGS.IDP_ENTITY_ID_REQUIRED;
    }
    if (!values.singleSignOnServiceUrl) {
      errors.singleSignOnServiceUrl = STRINGS.SINGLE_SIGN_ON_SERVICE_URL_REQUIRED;
    }
    if (!values.signingCertificate) {
      errors.signingCertificate = STRINGS.SIGNING_CERTIFICATE_REQUIRED;
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
      setCallbackUrl(res.callbackUrl);
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
            <VisualGreenRings scale={2.8} icon={VSquareIcon} />
          </VisualWrapper>

          <FlexColumn $gap={16}>
            <FormTitle>{STRINGS.PROVIDER_CREATED_SUCCESS}</FormTitle>
            <FormSubtitle>{STRINGS.PROVIDER_CREATED_SUCCESS_SUBTITLE(provider?.displayName)}</FormSubtitle>
          </FlexColumn>
        </FlexColumn>

        <ConstrainedWidth>
          <FlexColumn $gap={16}>
            {callbackUrl && (
              <FlexColumn $gap={8}>
                <Note status={StatusType.Default} message={STRINGS.CALLBACK_URL_INSTRUCTIONS(provider.displayName)} />
                <CliCommand value={callbackUrl} onDone={() => setHasCopiedCallbackUrl(true)} />
              </FlexColumn>
            )}

            <FlexColumn $gap={8}>
              <FullWidthTooltip>
                <Tooltip text={!!callbackUrl && !hasCopiedCallbackUrl ? STRINGS.COPY_CALLBACK_URL_TOOLTIP : undefined}>
                  <ButtonTab label={`${STRINGS.LOGIN_WITH} SAML`} loading={loading} disabled={!!callbackUrl && !hasCopiedCallbackUrl} onClick={() => onLogin(providerId, LoginType.SAML)} />
                </Tooltip>
              </FullWidthTooltip>
            </FlexColumn>
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
          <Input
            name='idpEntityId'
            value={formData.idpEntityId}
            onChange={(e) => handleFormChange(e.target.name, e.target.value)}
            errorMessage={formErrors.idpEntityId}
            label={STRINGS.IDP_ENTITY_ID}
            required
          />
          <Input
            name='singleSignOnServiceUrl'
            value={formData.singleSignOnServiceUrl}
            onChange={(e) => handleFormChange(e.target.name, e.target.value)}
            errorMessage={formErrors.singleSignOnServiceUrl}
            label={STRINGS.SINGLE_SIGN_ON_SERVICE_URL}
            required
          />
          <Input
            name='signingCertificate'
            value={formData.signingCertificate}
            onChange={(e) => handleFormChange(e.target.name, e.target.value)}
            errorMessage={formErrors.signingCertificate}
            label={STRINGS.SIGNING_CERTIFICATE}
            required
          />
        </FlexColumn>
      </ConstrainedWidth>

      <FlexColumn>
        {error && <ErrorText>{error}</ErrorText>}
        <Button type='submit' loading={loading} label={STRINGS.CREATE_PROVIDER} />
      </FlexColumn>
    </FormContainer>
  );
};

export { SamlForm, type SamlFormProps };
