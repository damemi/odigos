export const SIGNIN_TITLE = 'Sign In';
export const SIGNUP_TITLE = 'Set Up Odigos Central';
export const SIGNIN_SUBTITLE = 'Sign in to your Odigos Central account.';
export const SIGNUP_SUBTITLE = 'Create the first user for this environment by entering your email and password.';
export const EMAIL_LABEL = 'Email address';
export const PASSWORD_LABEL = 'Password';
export const CONFIRM_PASSWORD_LABEL = 'Confirm Password';
export const SIGNIN_BUTTON = 'Sign In';
export const SIGNUP_BUTTON = 'Sign Up';
export const ADMIN_NOTE_TITLE = 'This is the primary admin account for your entire setup. Choose an email that you want to use as the main point of control.';
export const AUTH_ERROR_TITLE = 'Authentication Error';
export const PASSWORDS_DO_NOT_MATCH = 'Passwords do not match';
export const NAME = 'Name';
export const TEAM_NAME = 'Team Name';
export const TEAM_NAME_PLACEHOLDER = 'Enter team name';
export const USERS_TAB = 'Users';
export const TEAMS_TAB = 'Teams';
export const COMPUTE_PLATFORMS_TAB = 'Compute Platforms';
export const CONNECTIONS_TAB = 'Connections';
export const DESCRIPTION = 'Description';
export const TYPE = 'Type';
export const CREATE_USER_BUTTON = 'CREATE USER +';
export const CREATE_TEAM_BUTTON = 'CREATE TEAM +';
export const ROLE_COLUMN = 'Role';
export const ACTIONS_COLUMN = 'Actions';
export const USER_LABEL = 'User';
export const TEAM_LABEL = 'Team';
export const PLATFORM_LABEL = 'Platform';
export const DASH = '—';
export const ERROR_TITLE = 'Error';
export const ID = 'ID';
export const UNIQUE_ID = 'Unique ID';
export const STATUS = 'Status';
export const CONNECTION_ALIVE = 'Connection Live';
export const CONNECTION_LOST = 'Connection Lost';

// Update Password Page
export const UPDATE_PASSWORD_TITLE = 'Update Your Password';
export const UPDATE_PASSWORD_SUBTITLE = 'Set a new password to continue.';
export const NEW_PASSWORD_LABEL = 'New Password';
export const UPDATE_PASSWORD_BUTTON = 'Update Password';
export const UPDATING_PASSWORD_BUTTON = 'Updating...';
export const EMAIL_PLACEHOLDER = 'Email';
export const NEW_PASSWORD_PLACEHOLDER = 'New Password';
export const CONFIRM_PASSWORD_PLACEHOLDER = 'Confirm Password';

// Create User Modal
export const CREATE_USER_MODAL_TITLE = 'Create New User';
export const CREATE_USER_EMAIL_REQUIRED = 'Email is required';
export const CREATE_USER_EMAIL_INVALID = 'Please enter a valid email address';
export const CREATE_USER_PASSWORD_REQUIRED = 'Password is required';
export const CREATE_USER_PASSWORD_MIN = 'Password must be at least 8 characters long';
export const CREATE_USER_PASSWORDS_MISMATCH = 'Passwords do not match';
export const CREATE_USER_SUCCESS = 'User has been created successfully';
export const CREATE_USER_ERROR = 'Failed to create user. Please try again.';
export const CREATE_USER_EMAIL_LABEL = 'Email Address';
export const CREATE_USER_PASSWORD_LABEL = 'Password';
export const CREATE_USER_CONFIRM_PASSWORD_LABEL = 'Confirm Password';
export const CREATE_USER_ROLE_LABEL = 'Role';
export const CREATE_USER_NOTES_LABEL = 'Notes (Optional)';
export const CREATE_USER_NOTES_PLACEHOLDER = 'Add any additional notes about this user';
export const CREATE_USER_CANCEL = 'Cancel';
export const CREATE_USER_SUBMIT = 'Create User';
export const CREATING = 'Creating...';
export const CREATE_USER_ROLE_OWNER_DESC = 'Full access to all features and settings. Can manage users, teams, and system configuration.';
export const CREATE_USER_ROLE_ADMIN_DESC = 'Administrative access to manage users, teams, and most system settings.';
export const CREATE_USER_ROLE_MAINTAINER_DESC = 'Can manage data sources, destinations, and instrumentation rules. Limited user management.';
export const CREATE_USER_ROLE_VIEWER_DESC = 'Read-only access to view data sources, destinations, and system status.';

// Create Team Modal
export const CREATE_TEAM_MODAL_TITLE = 'Create New Team';
export const CREATE_TEAM_SUBMIT = 'Create Team';

// Success state for Create User Modal
export const USER_CREATED_SUCCESS_TITLE = 'User Created Successfully!';
export const USER_CREATED_SUCCESS_SUBTITLE = (email: string) => `A new user account has been created for ${email}.\nTo activate the account, share the temporary password below with the user.`;
export const USER_CREATED_SUCCESS_PASSWORD_NOTE =
  'This password is for first-time login only and must be changed after signing in. You won’t be able to view it again, make sure to copy and share it now';
export const USER_CREATED_SUCCESS_DONE_LABEL = 'DONE';

// Success state for Create Team Modal
export const TEAM_CREATED_SUCCESS_TITLE = 'Team Created Successfully!';
export const TEAM_CREATED_SUCCESS_SUBTITLE = (name: string) => `Team "${name}" has been created successfully.`;
export const TEAM_CREATED_SUCCESS_DONE_LABEL = 'DONE';

// Teams and Compute Platforms
export const TEAMS_LABEL = 'Teams';
export const TEAMS_PLACEHOLDER = 'Choose Team from list';
export const COMPUTE_PLATFORM_LABEL = 'Compute platform';
export const COMPUTE_PLATFORM_PLACEHOLDER = 'Choose Compute platform from list';
export const MEMBER = 'Member';
export const MEMBERS_LABEL = 'Members';
export const MEMBERS_PLACEHOLDER = 'Choose Member from list';

// Create User Form
export const CREATE_USER_FORM_DESCRIPTION_1 = 'Add a new user to your Odigos environment.';
export const CREATE_USER_FORM_DESCRIPTION_2 = 'Assign their role and link them to relevant members or compute platforms.';

// Create Team Form
export const CREATE_TEAM_FORM_DESCRIPTION_1 = 'Add a new team to your Odigos environment.';
export const CREATE_TEAM_FORM_DESCRIPTION_2 = 'Link them to relevant teams or compute platforms.';
export const CREATE_TEAM_FORM_DESCRIPTION_PLACEHOLDER = 'Add any additional notes about this team';

// Access Management
export const ACCESS_MANAGEMENT_TITLE = 'Access Management';

export const USERS_COUNT = (count: number) => `${count} ${USER_LABEL}${count === 1 ? '' : 's'}`;
export const MEMBERS_COUNT = (count: number) => `${count} ${MEMBER}${count === 1 ? '' : 's'}`;
export const TEAMS_COUNT = (count: number) => `${count} ${TEAM_LABEL}${count === 1 ? '' : 's'}`;
export const PLATFORMS_COUNT = (count: number) => `${count} ${PLATFORM_LABEL}${count === 1 ? '' : 's'}`;
