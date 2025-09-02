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
export const DONE = 'Done';

// Auth strings
export const ACCESS_MANAGEMENT = 'Access Management';
export const SIGN_IN = 'Sign In';
export const SIGN_IN_SUBTITLE = 'Sign in to your Odigos Tower account.';
export const SIGN_UP = 'Set Up';
export const SIGN_UP_SUBTITLE = 'Create the first user for this environment by entering your email and password.';
export const SIGN_UP_ADMIN_DISCLAIMER = 'This is the primary admin account for your entire setup. Choose an email that you want to use as the main point of control.';
export const SIGN_UP_PASSWORD_DISCLAIMER = 'This password is for first-time login only and must be changed after signing in. You won’t be able to view it again, make sure to copy and share it now';
export const AUTH_ERROR = 'Authentication Error';

// Auth form strings
export const EMAIL = 'Email Address';
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
export const UPDATE_PASSWORD = 'Update Password';
export const UPDATE_YOUR_PASSWORD = 'Update Your Password';
export const UPDATE_PASSWORD_SUBTITLE = 'Set a new password to continue.';

// CP strings
export const CONNECTIONS = 'Connections';
export const COMPUTE_PLATFORMS = 'Compute Platforms';
export const COMPUTE_PLATFORM = 'Compute Platform';
export const PLATFORM = 'Platform';
export const PLATFORMS_COUNT = (count: number) => `${count} ${PLATFORM}${count === 1 ? '' : 's'}`;
export const CONNECTION_ALIVE = 'Connection Live';
export const CONNECTION_LOST = 'Connection Lost';
export const CHOOSE_COMPUTE_PLATFORM = 'Choose Compute Platform';

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
