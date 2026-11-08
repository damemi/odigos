import type { ComputePlatform } from './compute-platforms';

export enum Role {
  OWNER = 'OWNER',
  ADMIN = 'ADMIN',
  VIEWER = 'VIEWER',
  MAINTAINER = 'MAINTAINER',
  NO_ACCESS = 'NO_ACCESS',
}

export enum SignInStatus {
  SUCCESS = 'SUCCESS',
  PASSWORD_UPDATE_REQUIRED = 'PASSWORD_UPDATE_REQUIRED',
  ACCESS_DENIED = 'ACCESS_DENIED',
}

export interface SystemConfig {
  isInitialized: boolean;
  version: string;
}

export interface GeneralResult {
  success: boolean;
  error?: string;
}

export interface IdentityProviderResult {
  success: boolean;
  error?: string;
  callbackUrl?: string;
}

export interface SignInPayload {
  idToken?: string;
  accessToken?: string;
  user?: User;
  status: SignInStatus;
}

export interface CreateUserResult {
  success: boolean;
  userID: string;
  generatedPassword?: string;
}

export interface ResetUserPasswordResult {
  success: boolean;
  error?: string;
  generatedPassword?: string;
}

export interface User {
  id: string;
  email: string;
  username: string;
  role: Role;
  needsPasswordChange?: boolean;
  teams?: Team[];
  computePlatforms?: ComputePlatform[];
}

export interface UserInput {
  email: string;
  password?: string;
  role: Role;
  addTeamIds: string[];
  removeTeamIds: string[];
  addComputePlatformIds?: string[];
  removeComputePlatformIds?: string[];
}

export interface Team {
  id: string;
  name: string;
  description?: string;
  users?: User[];
  computePlatforms?: ComputePlatform[];
  createdAt?: string;
  updatedAt?: string;
}

export interface TeamInput {
  name: string;
  description?: string;
  addUserIds: string[];
  removeUserIds: string[];
  addComputePlatformIds: string[];
  removeComputePlatformIds: string[];
}
