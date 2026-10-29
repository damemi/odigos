// General/Common strings
export const DASH = '—';
export const ID = 'ID';
export const UNIQUE_ID = 'Unique ID';
export const ERROR = 'Error';
export const STATUS = 'Status';
export const TYPE = 'Type';
export const NAME = 'Name';
export const DESCRIPTION = 'Description';
export const NOTES = 'Notes';
export const ROLE = 'Role';
export const ACTIONS = 'Actions';
export const CREATING = 'Creating';
export const UPDATING = 'Updating';
export const CANCEL = 'Cancel';
export const DELETE = 'Delete';
export const DONE = 'Done';
export const APPROVE = 'Approve';
export const DENY = 'Deny';
export const DISCARD_CHANGES_TITLE = 'Discard changes?';
export const DISCARD_CHANGES_DESCRIPTION = "You have unsaved changes.\nIf you cancel now, your changes won't be saved.";

// Auth strings
export const ACCESS_MANAGEMENT = 'Access Management';
export const SIGN_IN = 'Sign In';
export const SIGN_IN_SUBTITLE_1 = 'Log in to your Odigos Central account.';
export const SIGN_IN_SUBTITLE_2 = 'Log in to your Odigos Central account using an email and password.';
export const SIGN_OUT = 'Sign out';
export const SIGN_UP = 'Sign Up';
export const SIGN_UP_SUBTITLE_1 = 'Create your first user for this environment.';
export const SIGN_UP_SUBTITLE_2 = 'Create your first user using an email and password.';
export const SIGN_UP_ADMIN_TITLE = 'Set Up Your Admin Account';
export const SIGN_UP_ADMIN_SUBTITLE_1 = 'Create the first user for this environment. This account will act as the main admin and manage all workspace settings';
export const SIGN_UP_ADMIN_SUBTITLE_2 = 'Create your first user using an email and password. This account will serve as the main admin for this environment.';
export const SIGN_UP_ADMIN_DISCLAIMER = 'This account will be used as the admin for setting up your environment.';
export const SIGN_UP_PASSWORD_DISCLAIMER = 'This password is for first-time login only and must be changed after signing in. You won’t be able to view it again, make sure to copy and share it now';
export const AUTH_ERROR = 'Authentication Error';
export const ACCESS_DENIED_ERROR = "You don't have access to Odigos Central. Contact your administrator.";
export const LOGIN_WITH = 'Login with';
export const CONTINUE_WITH = 'Continue with';
export const CONTINUE_WITH_EMAIL = 'Continue with Email';
export const PROVIDER_CREATED_SUCCESS = 'Provider Created Successfully!';
export const PROVIDER_CREATED_SUCCESS_SUBTITLE = (displayName: string) => `Use your ${displayName} credentials to authenticate and register the admin account for this environment`;
export const PROVIDER_REDIRECT_NOTICE = (displayName: string) => `Use your ${displayName} credentials to authenticate and register the admin account for this environment`;
export const CALLBACK_URL_LABEL = 'Callback URL (ACS URL)';
export const CALLBACK_URL_INSTRUCTIONS = (displayName: string) => `Copy this URL and add it to your ${displayName} application settings as the callback or ACS URL.`;
export const COPY_CALLBACK_URL_TOOLTIP = 'Please copy the callback URL above before proceeding to login.';

export const CLIENT_ID = 'Client ID';
export const CLIENT_ID_REQUIRED = 'Client ID is required';
export const CLIENT_ID_TOOLTIP = 'The Client ID is a unique identifier for the application.';
export const CLIENT_SECRET = 'Client Secret';
export const CLIENT_SECRET_REQUIRED = 'Client Secret is required';
export const CLIENT_SECRET_TOOLTIP = 'The Client Secret is a secret key for the application.';
export const DISCOVERY_URL = 'Discovery URL';
export const DISCOVERY_URL_REQUIRED = 'Discovery URL is required';
export const DISCOVERY_URL_TOOLTIP = 'The Discovery URL is used to import the configuration/metadata of the OIDC provider. Use {tenantid} as a placeholder for the Tenant ID.';
export const TENANT_ID = 'Tenant ID';
export const TENANT_ID_REQUIRED = 'Tenant ID is required';
export const TENANT_ID_TOOLTIP = 'An identifier that represents the organization. Substituted for {tenantid} in the Discovery URL and issuer when those values use that placeholder.';
export const TENANT_ID_OPTIONAL_TOOLTIP = 'Optional for this discovery URL. Only needed when the Discovery URL or issuer contains a {tenantid} placeholder.';
export const CREATE_PROVIDER = 'Create Provider';
export const CREATE_PROVIDER_TITLE = 'Set Up Identity Provider';
export const CREATE_PROVIDER_SUBTITLE = 'Configure your identity provider for user authentication.';
export const IDP_ENTITY_ID = 'IdP Entity ID';
export const IDP_ENTITY_ID_REQUIRED = 'IdP Entity ID is required';
export const SINGLE_SIGN_ON_SERVICE_URL = 'Single Sign-On Service URL';
export const SINGLE_SIGN_ON_SERVICE_URL_REQUIRED = 'Single Sign-On Service URL is required';
export const SIGNING_CERTIFICATE = 'Signing Certificate';
export const SIGNING_CERTIFICATE_REQUIRED = 'Signing Certificate is required';

// Auth form strings
export const EMAIL = 'Email';
export const EMAIL_PLACEHOLDER = 'john.doe@example.com';
export const EMAIL_REQUIRED = 'Email is required';
export const EMAIL_INVALID = 'Please enter a valid email address';
export const PASSWORD = 'Password';
export const PASSWORD_PLACEHOLDER = '••••••••••';
export const PASSWORD_REQUIRED = 'Password is required';
export const PASSWORD_INVALID = 'Password must be at least 8 characters long';
export const CONFIRM_PASSWORD = 'Confirm Password';
export const PASSWORDS_DO_NOT_MATCH = 'Passwords do not match';
export const NEW_PASSWORD = 'New Password';
export const TEMPORARY_PASSWORD = 'Temporary Password';
export const TEMPORARY_PASSWORD_REQUIRED = 'Temporary password is required';
export const TEMPORARY_PASSWORD_NOTE = 'Enter the temporary password your admin shared with you.';
export const UPDATE_PASSWORD = 'Update Password';
export const UPDATE_YOUR_PASSWORD = 'Update Your Password';
export const UPDATE_PASSWORD_SUBTITLE = 'Set a new password to continue.';

// Auth code query param
export const AUTH_CODE_QUERY_PARAM = 'code';
// Stored after ACCESS_DENIED so the next SSO click can clear the Azure/Okta browser session first.
export const PENDING_IDP_LOGOUT_TOKEN_KEY = 'odigos_pending_idp_logout_token';
// Provider to auto-start after returning from that deferred IdP logout.
export const AUTO_SSO_PROVIDER_KEY = 'odigos_auto_sso_provider';

// CP strings
export const CONNECTIONS = 'Connections';
export const COMPUTE_PLATFORMS = 'Compute Platforms';
export const COMPUTE_PLATFORM = 'Compute Platform';
export const PLATFORM = 'Platform';
export const PLATFORMS_COUNT = (count: number) => `${count} ${PLATFORM}${count === 1 ? '' : 's'}`;
export const CONNECTION_ALIVE = 'Connection Live';
export const CONNECTION_LOST = 'Connection Lost';
export const CHOOSE_COMPUTE_PLATFORM = 'Choose Compute Platform';
export const CREATE_NEW = 'Create new';
export const CREATE_NEW_COMING_SOON = (type: string) => `"${type}" flow is coming soon`;

// User/Member strings
export const USER = 'User';
export const USERS = 'Users';
export const USERS_COUNT = (count: number) => `${count} ${USER}${count === 1 ? '' : 's'}`;
export const MEMBER = 'Member';
export const MEMBERS = 'Members';
export const MEMBERS_COUNT = (count: number) => `${count} ${MEMBER}${count === 1 ? '' : 's'}`;
export const CHOOSE_MEMBER = 'Choose Member';
export const CREATE_USER = 'Create User';
export const USER_CREATED_SUCCESS = 'User Created Successfully!';
export const USER_CREATED_SUCCESS_SUBTITLE = (email: string) => `A new user account has been created for ${email}.\nTo activate the account, share the temporary password below with the user.`;
export const CREATE_USER_ERROR = 'Failed to create user. Please try again.';
export const CREATE_USER_FORM_DESCRIPTION_1 = 'Add a new user to your Odigos environment.';
export const CREATE_USER_FORM_DESCRIPTION_2 = 'Assign their role and link them to relevant members or compute platforms.';
export const EDIT_USER = 'Edit user';
export const DELETE_USER = 'Delete user';
export const DELETE_USER_CONFIRM = (name: string) => `Are you sure you want to delete "${name}"?`;

// Team strings
export const TEAM = 'Team';
export const TEAMS = 'Teams';
export const TEAMS_COUNT = (count: number) => `${count} ${TEAM}${count === 1 ? '' : 's'}`;
export const TEAM_NAME = 'Team Name';
export const TEAM_NAME_PLACEHOLDER = 'Enter team name';
export const CHOOSE_TEAM = 'Choose Team';
export const CREATE_TEAM = 'Create Team';
export const TEAM_CREATED_SUCCESS = 'Team Created Successfully!';
export const TEAM_CREATED_SUCCESS_SUBTITLE = (name: string) => `Team "${name}" has been created successfully.`;
export const CREATE_TEAM_FORM_DESCRIPTION_1 = 'Add a new team to your Odigos environment.';
export const CREATE_TEAM_FORM_DESCRIPTION_2 = 'Link them to relevant teams or compute platforms.';

// Role mapping strings
export const ROLE_MAPPING = 'Role Mapping';
export const ROLE_MAPPINGS = 'Role Mappings';
export const GROUP = 'Group';
export const CREATE_ROLE_MAPPING = 'Create Role Mapping';
export const CLAIM_VALUE = 'Group';
export const CLAIM_VALUE_PLACEHOLDER = 'Azure group object ID or name';
export const CLAIM_VALUE_REQUIRED = 'Group is required';
export const CLAIM_VALUE_TOOLTIP = 'The group identifier emitted by your identity provider (e.g. Azure/Entra group object ID or display name).';
export const DISPLAY_NAME = 'Display Name';
export const DISPLAY_NAME_PLACEHOLDER = 'e.g. odigos-admins';
export const DISPLAY_NAME_TOOLTIP = 'Optional friendly name shown in the table instead of the group ID. Does not affect matching.';
export const CLAIM_NAME = 'Claim Name';
export const CLAIM_NAME_PLACEHOLDER = 'groups';
export const CLAIM_NAME_TOOLTIP = 'The token claim that carries group membership. Defaults to "groups".';
export const EDIT_ROLE_MAPPING = 'Edit role mapping';
export const DELETE_ROLE_MAPPING = 'Delete role mapping';
export const DELETE_MAPPING = 'Delete Mapping';
export const DELETE_ROLE_MAPPING_CONFIRM = (name: string) => `Are you sure you want to delete the mapping for "${name}"?`;
export const CREATE_ROLE_MAPPING_ERROR = 'Failed to create role mapping. Please try again.';
export const CREATE_ROLE_MAPPING_FORM_DESCRIPTION_1 = 'Map an identity provider group to an Odigos role.';
export const CREATE_ROLE_MAPPING_FORM_DESCRIPTION_2 = 'Users in this group are granted the selected role on sign-in. Highest privilege wins across groups.';

// Identity provider strings (Access management)
export const IDENTITY_PROVIDERS = 'Identity Providers';
export const PROVIDER = 'Provider';
export const PROTOCOL = 'Protocol';
export const IDENTIFIER = 'Identifier';
export const IDP_SET_UP = 'Set up';
export const IDP_DISABLED = 'Disabled';
export const IDP_NOT_SET_UP = 'Not set up';
export const IDP_NONE_CONNECTED = 'No identity provider is connected yet. Users sign in with email and password until you connect one.';
export const IDP_LOAD_ERROR_TITLE = "Couldn't load identity providers";
export const IDP_LOAD_ERROR_SUBTITLE = 'Check that Keycloak is reachable, then try again.';
export const RETRY = 'Try again';
export const EDIT_IDENTITY_PROVIDER = (displayName: string) => `Edit ${displayName}`;
export const CONNECT_IDENTITY_PROVIDER = (displayName: string) => `Connect ${displayName}`;
export const IDP_PROTOCOL_LOCKED = (protocol: string) => `Set up with ${protocol}. Changing protocol isn't supported because it would remove this provider's role mappings.`;
export const SECRET_STORED_PLACEHOLDER = 'Stored. Leave blank to keep it.';
export const CLIENT_SECRET_REQUIRED_ON_CHANGE = 'Enter the client secret again when the discovery URL, tenant ID or client ID changes';
export const CERTIFICATE_STORED_PLACEHOLDER = 'Stored. Leave blank to keep it, or paste a new certificate.';
export const SIGNING_CERTIFICATE_PLACEHOLDER = 'Base64 certificate body, without BEGIN/END lines';
export const IDP_CALLBACK_URL_PENDING = (displayName: string) => `Save to get the callback URL to register in ${displayName}.`;
export const IDP_SAVED = (displayName: string) => `${displayName} saved. Register the callback URL below in ${displayName} if you haven't yet.`;
export const IDP_SAVE_ERROR = (displayName: string) => `Couldn't save ${displayName}. Check the values and that Keycloak is reachable, then try again.`;
export const IDP_NOT_SET_UP_SIGN_IN = (displayName: string) => `${displayName} sign-in isn't set up. Sign in with email, or ask an Admin to connect ${displayName} in Access management.`;

// App info strings
export const VERSION_LABEL = (version: string) => `Version: ${version}`;
