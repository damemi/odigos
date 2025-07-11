import { Role } from '@/types';
import React from 'react';
import styled, { css } from 'styled-components';

export type BadgeVariant = Role;

const badgeStyles = {
  [Role.VIEWER]: css`
    background: #242424;
    color: #b8b8b8;
  `,
  [Role.ADMIN]: css`
    background: #3b170d;
    color: #ffb160;
  `,
  [Role.MAINTAINER]: css`
    background: #472300;
    color: #e9cf35;
  `,
  [Role.OWNER]: css`
    background: #181944;
    color: #aabef7;
  `,
};

const StyledBadge = styled.span<{ variant: BadgeVariant }>`
  border-radius: 36px;
  width: fit-content;
  padding: 4px 8px;
  font-family: ${({ theme }) => theme.font_family.secondary};
  font-size: 12px;
  text-transform: uppercase;
  ${({ variant }) => badgeStyles[variant]}
`;

export interface BadgeProps {
  variant: BadgeVariant;
  children: React.ReactNode;
  className?: string;
}

export const RoleBadge: React.FC<BadgeProps> = ({ variant, children, className }) => (
  <StyledBadge variant={variant} className={className}>
    {children}
  </StyledBadge>
);
