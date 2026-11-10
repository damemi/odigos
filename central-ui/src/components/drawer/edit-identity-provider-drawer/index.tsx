import React, { useEffect, useEffectEvent, useState, type FC } from 'react';
import { StatusType } from '@odigos/ui-kit/types';
import { KeyIcon } from '@odigos/ui-kit/icons';
import * as STRINGS from '@/utils/constants/strings';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { BUTTON_TEXTS } from '@odigos/ui-kit/constants';
import { isOidcTenantIdRequired } from '@/utils/constants/auth-providers';
import { ButtonVariants, CliCommand, Drawer, FlexColumn, Input, Note, Segment, SegmentVariant, TextArea, Typography, TypographyColor, TypographySize, WarningModal } from '@odigos/ui-kit/components';
import {
  IdentityProviderProtocol,
  LoginType,
  type AuthProviderOption,
  type IdentityProvider,
  type OidcIdentityProviderInput,
  type SamlIdentityProviderInput,
} from '@/types';

interface EditIdentityProviderDrawerProps {
  provider: AuthProviderOption;
  // Undefined until the provider is connected.
  existing?: IdentityProvider;
  saving: boolean;
  justSaved: boolean;
  // Each returns the error message, or undefined on success.
  onSaveOidc: (input: OidcIdentityProviderInput) => Promise<string | undefined>;
  onSaveSaml: (input: SamlIdentityProviderInput) => Promise<string | undefined>;
  onClose: () => void;
}

const PROTOCOL_LABELS: Record<IdentityProviderProtocol, string> = {
  [IdentityProviderProtocol.OIDC]: 'OIDC',
  [IdentityProviderProtocol.SAML]: 'SAML',
};

const defaultProtocol = (provider: AuthProviderOption, existing?: IdentityProvider): IdentityProviderProtocol => {
  if (existing) return existing.protocol;
  return provider.defaultLoginType === LoginType.SAML ? IdentityProviderProtocol.SAML : IdentityProviderProtocol.OIDC;
};

const EditIdentityProviderDrawer: FC<EditIdentityProviderDrawerProps> = ({ provider, existing, saving, justSaved, onSaveOidc, onSaveSaml, onClose }) => {
  const [protocol, setProtocol] = useState<IdentityProviderProtocol>(defaultProtocol(provider, existing));
  const [saveError, setSaveError] = useState<string | null>(null);
  const [cancelWarningOpen, setCancelWarningOpen] = useState(false);
  const [cancelApproveCallback, setCancelApproveCallback] = useState<(() => void) | null>(null);

  // The backend keeps a stored secret or certificate when the field is left blank.
  const isOidcStored = existing?.protocol === IdentityProviderProtocol.OIDC;
  const isSamlStored = existing?.protocol === IdentityProviderProtocol.SAML;
  const secretStored = isOidcStored && existing.hasClientSecret;
  const certificateStored = isSamlStored && existing.hasSigningCertificate;

  const oidc = useGenericForm<OidcIdentityProviderInput>({
    providerId: provider.providerId,
    discoveryUrl: (isOidcStored && existing.discoveryUrl) || provider.discoveryUrl || '',
    tenantId: (isOidcStored && existing.tenantId) || '',
    clientId: (isOidcStored && existing.clientId) || '',
    clientSecret: '',
  });
  const saml = useGenericForm<SamlIdentityProviderInput>({
    providerId: provider.providerId,
    idpEntityId: (isSamlStored && existing.idpEntityId) || '',
    singleSignOnServiceUrl: (isSamlStored && existing.singleSignOnServiceUrl) || '',
    signingCertificate: '',
  });

  // The backend only keeps the stored secret while the provider still points at the same
  // identity provider app, so changing any of these fields needs the secret again.
  const appChanged =
    isOidcStored &&
    (oidc.formData.discoveryUrl.trim() !== (existing.discoveryUrl || '') ||
      oidc.formData.tenantId.trim() !== (existing.tenantId || '') ||
      oidc.formData.clientId.trim() !== (existing.clientId || ''));
  const canKeepSecret = secretStored && !appChanged;

  const isOidc = protocol === IdentityProviderProtocol.OIDC;
  const isFormDirty = isOidc ? oidc.isFormDirty : saml.isFormDirty;
  const tenantIdRequired = isOidcTenantIdRequired(oidc.formData.discoveryUrl, provider.providerId);

  const validate = (): boolean => {
    if (isOidc) {
      const values = oidc.formData;
      const errors: typeof oidc.formErrors = {};
      if (!values.discoveryUrl.trim()) errors.discoveryUrl = STRINGS.DISCOVERY_URL_REQUIRED;
      if (tenantIdRequired && !values.tenantId.trim()) errors.tenantId = STRINGS.TENANT_ID_REQUIRED;
      if (!values.clientId.trim()) errors.clientId = STRINGS.CLIENT_ID_REQUIRED;
      if (!canKeepSecret && !values.clientSecret) errors.clientSecret = secretStored ? STRINGS.CLIENT_SECRET_REQUIRED_ON_CHANGE : STRINGS.CLIENT_SECRET_REQUIRED;
      oidc.handleErrorChange(undefined, undefined, errors);
      return Object.keys(errors).length === 0;
    }

    const values = saml.formData;
    const errors: typeof saml.formErrors = {};
    if (!values.idpEntityId.trim()) errors.idpEntityId = STRINGS.IDP_ENTITY_ID_REQUIRED;
    if (!values.singleSignOnServiceUrl.trim()) errors.singleSignOnServiceUrl = STRINGS.SINGLE_SIGN_ON_SERVICE_URL_REQUIRED;
    if (!certificateStored && !values.signingCertificate.trim()) errors.signingCertificate = STRINGS.SIGNING_CERTIFICATE_REQUIRED;
    saml.handleErrorChange(undefined, undefined, errors);
    return Object.keys(errors).length === 0;
  };

  const handleSave = async () => {
    setSaveError(null);
    if (!validate()) return;

    const error = isOidc
      ? await onSaveOidc({
          ...oidc.formData,
          discoveryUrl: oidc.formData.discoveryUrl.trim(),
          tenantId: oidc.formData.tenantId.trim(),
          clientId: oidc.formData.clientId.trim(),
        })
      : await onSaveSaml({
          ...saml.formData,
          idpEntityId: saml.formData.idpEntityId.trim(),
          singleSignOnServiceUrl: saml.formData.singleSignOnServiceUrl.trim(),
          signingCertificate: saml.formData.signingCertificate.trim(),
        });
    if (error) {
      console.error(error);
      setSaveError(STRINGS.IDP_SAVE_ERROR(provider.displayName));
    }
  };

  const handleWithCancelWarning = (callback: () => void) => {
    if (isFormDirty) {
      setCancelWarningOpen(true);
      setCancelApproveCallback(() => callback);
    } else {
      callback();
    }
  };

  // The kit Drawer has no Escape handling and its close icon can't take focus, so Escape closes it here.
  const onEscape = useEffectEvent(() => handleWithCancelWarning(onClose));
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !cancelWarningOpen) onEscape();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [cancelWarningOpen]);

  const handleReset = () => {
    oidc.resetFormData();
    saml.resetFormData();
    setProtocol(defaultProtocol(provider, existing));
    setSaveError(null);
  };

  const footerNote = saveError
    ? { status: StatusType.Error, message: saveError }
    : justSaved && !isFormDirty
      ? { status: StatusType.Success, message: STRINGS.IDP_SAVED(provider.displayName) }
      : undefined;

  return (
    <>
      <Drawer
        isOpen
        // Remounted with the stored values after a save; don't replay the slide-in.
        disableAnimation={justSaved}
        header={{
          onClose: () => handleWithCancelWarning(onClose),
          icon: provider.icon ?? KeyIcon,
          title: existing ? STRINGS.EDIT_IDENTITY_PROVIDER(provider.displayName) : STRINGS.CONNECT_IDENTITY_PROVIDER(provider.displayName),
        }}
        footer={{
          note: footerNote,
          actions: [
            {
              'data-id': 'drawer-cancel',
              variant: ButtonVariants.Secondary,
              label: BUTTON_TEXTS.CANCEL,
              disabled: saving || !isFormDirty,
              onClick: () => handleWithCancelWarning(handleReset),
            },
            {
              'data-id': 'drawer-save',
              variant: ButtonVariants.Primary,
              label: BUTTON_TEXTS.SAVE,
              loading: saving,
              disabled: saving || !isFormDirty,
              onClick: handleSave,
            },
          ],
        }}
      >
        <FlexColumn $gap={16}>
          <FlexColumn $gap={8}>
            <Segment
              fullWidth
              variant={SegmentVariant.Filled}
              disabled={!!existing || saving}
              options={Object.values(IdentityProviderProtocol).map((value) => ({ value, label: PROTOCOL_LABELS[value] }))}
              selected={protocol}
              setSelected={(value: IdentityProviderProtocol) => {
                setSaveError(null);
                setProtocol(value);
              }}
            />
            {existing && (
              <Typography size={TypographySize.XXS} color={TypographyColor.Secondary}>
                {STRINGS.IDP_PROTOCOL_LOCKED(PROTOCOL_LABELS[existing.protocol])}
              </Typography>
            )}
          </FlexColumn>

          {isOidc ? (
            <FlexColumn $gap={12}>
              <Input
                name='discoveryUrl'
                label={STRINGS.DISCOVERY_URL}
                tooltip={STRINGS.DISCOVERY_URL_TOOLTIP}
                required
                value={oidc.formData.discoveryUrl}
                errorMessage={oidc.formErrors.discoveryUrl}
                disabled={saving}
                onChange={(e) => oidc.handleFormChange('discoveryUrl', e.target.value)}
              />
              <Input
                name='tenantId'
                label={STRINGS.TENANT_ID}
                tooltip={tenantIdRequired ? STRINGS.TENANT_ID_TOOLTIP : STRINGS.TENANT_ID_OPTIONAL_TOOLTIP}
                required={tenantIdRequired}
                value={oidc.formData.tenantId}
                errorMessage={oidc.formErrors.tenantId}
                disabled={saving}
                onChange={(e) => oidc.handleFormChange('tenantId', e.target.value)}
              />
              <Input
                name='clientId'
                label={STRINGS.CLIENT_ID}
                tooltip={STRINGS.CLIENT_ID_TOOLTIP}
                required
                value={oidc.formData.clientId}
                errorMessage={oidc.formErrors.clientId}
                disabled={saving}
                onChange={(e) => oidc.handleFormChange('clientId', e.target.value)}
              />
              <Input
                name='clientSecret'
                type='password'
                autoComplete='new-password'
                label={STRINGS.CLIENT_SECRET}
                tooltip={STRINGS.CLIENT_SECRET_TOOLTIP}
                required={!canKeepSecret}
                placeholder={canKeepSecret ? STRINGS.SECRET_STORED_PLACEHOLDER : undefined}
                value={oidc.formData.clientSecret}
                errorMessage={oidc.formErrors.clientSecret}
                disabled={saving}
                onChange={(e) => oidc.handleFormChange('clientSecret', e.target.value)}
              />
            </FlexColumn>
          ) : (
            <FlexColumn $gap={12}>
              <Input
                name='idpEntityId'
                label={STRINGS.IDP_ENTITY_ID}
                required
                value={saml.formData.idpEntityId}
                errorMessage={saml.formErrors.idpEntityId}
                disabled={saving}
                onChange={(e) => saml.handleFormChange('idpEntityId', e.target.value)}
              />
              <Input
                name='singleSignOnServiceUrl'
                label={STRINGS.SINGLE_SIGN_ON_SERVICE_URL}
                required
                value={saml.formData.singleSignOnServiceUrl}
                errorMessage={saml.formErrors.singleSignOnServiceUrl}
                disabled={saving}
                onChange={(e) => saml.handleFormChange('singleSignOnServiceUrl', e.target.value)}
              />
              <TextArea
                name='signingCertificate'
                label={STRINGS.SIGNING_CERTIFICATE}
                required={!certificateStored}
                placeholder={certificateStored ? STRINGS.CERTIFICATE_STORED_PLACEHOLDER : STRINGS.SIGNING_CERTIFICATE_PLACEHOLDER}
                minHeight={120}
                value={saml.formData.signingCertificate}
                errorMessage={saml.formErrors.signingCertificate}
                disabled={saving}
                onChange={(e) => saml.handleFormChange('signingCertificate', e.target.value)}
              />
            </FlexColumn>
          )}

          <FlexColumn $gap={8}>
            <Typography size={TypographySize.XS}>{STRINGS.CALLBACK_URL_LABEL}</Typography>
            {existing ? (
              <>
                <Typography size={TypographySize.XXS} color={TypographyColor.Secondary}>
                  {STRINGS.CALLBACK_URL_INSTRUCTIONS(provider.displayName)}
                </Typography>
                <CliCommand value={existing.callbackUrl} />
              </>
            ) : (
              <Note status={StatusType.Default} message={STRINGS.IDP_CALLBACK_URL_PENDING(provider.displayName)} />
            )}
          </FlexColumn>
        </FlexColumn>
      </Drawer>

      <WarningModal
        isOpen={cancelWarningOpen}
        title={STRINGS.DISCARD_CHANGES_TITLE}
        description={STRINGS.DISCARD_CHANGES_DESCRIPTION}
        onClose={() => setCancelWarningOpen(false)}
        approveButton={{
          label: STRINGS.APPROVE,
          onClick: () => {
            cancelApproveCallback?.();
            setCancelWarningOpen(false);
          },
        }}
        denyButton={{
          label: STRINGS.DENY,
          onClick: () => setCancelWarningOpen(false),
        }}
      />
    </>
  );
};

export { EditIdentityProviderDrawer, type EditIdentityProviderDrawerProps };
