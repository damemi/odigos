export enum Role {
  OWNER = 'OWNER',
  ADMIN = 'ADMIN',
  VIEWER = 'VIEWER',
  MAINTAINER = 'MAINTAINER',
}

export enum SignInStatus {
  SUCCESS = 'SUCCESS',
  PASSWORD_UPDATE_REQUIRED = 'PASSWORD_UPDATE_REQUIRED',
}

export interface User {
  id: string;
  email: string;
  username: string;
  role: Role;
  needsPasswordChange: boolean;
  teams: Team[];
  computePlatforms: ComputePlatform[];
}

export interface Team {
  id: string;
  name: string;
  description?: string;
  createdAt: string;
  updatedAt: string;
  users: User[];
  computePlatforms: ComputePlatform[];
}

export interface ComputePlatform {
  id: string;
  name: string;
  type: string;
  status: string;
}

export interface SignInPayload {
  token: string;
  user?: User;
  status: SignInStatus;
}

export interface DeleteUserPayload {
  success: boolean;
  error?: string;
}

export interface SystemConfig {
  isInitialized: boolean;
  version: string;
}
