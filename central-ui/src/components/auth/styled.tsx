import styled from 'styled-components';
import { VISUAL_ODIGOS_LOGO_HEIGHT, VISUAL_ODIGOS_LOGO_WIDTH } from '@odigos/ui-kit/visuals';

export const FormContainer = styled.form<{ $gap?: number }>`
  width: ${VISUAL_ODIGOS_LOGO_WIDTH}px;
  height: ${VISUAL_ODIGOS_LOGO_HEIGHT}px;
  padding: 16px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: ${({ $gap = 12 }) => $gap}px;
`;

export const FormTitle = styled.h2`
  margin: 0;
  margin-top: 36px;
  padding: 0;
  font-family: ${({ theme }) => theme.font_family.primary};
  font-size: 20px;
  font-weight: 500;
  line-height: 26px;
  color: #f9f9f9;
  text-align: center;
`;

export const FormSubtitle = styled.p`
  max-width: 330px;
  margin: 0;
  padding: 0;
  font-family: ${({ theme }) => theme.font_family.primary};
  font-size: 14px;
  font-weight: 400;
  line-height: 21px;
  color: #b8b8b8;
  text-align: center;
`;

export const ConstrainedWidth = styled.div`
  width: 100%;
  max-width: 360px;
  margin: 0 auto auto auto;
`;

export const ErrorText = styled.p`
  margin: 0;
  font-family: ${({ theme }) => theme.font_family.primary};
  font-size: 12px;
  font-weight: 500;
  line-height: 16px;
  color: ${({ theme }) => theme.text.error};
  text-align: center;
`;
