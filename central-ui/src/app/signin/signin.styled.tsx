import { Text } from '@odigos/ui-kit/components';
import styled from 'styled-components';

export const LogoWrapper = styled.div`
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 32px;
`;

export const Container = styled.div`
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 2rem 1rem;
  position: relative;
  overflow: hidden;
`;

export const Card = styled.div`
  border-radius: 40px;
  border: 1px solid rgba(249, 249, 249, 0.24);
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.25);
  padding: 2.5rem;
  width: 100%;
  max-width: 560px;
`;

export const Title = styled(Text)`
  text-align: center;
  font-size: 24px;
  font-style: normal;
  font-weight: 600;
  line-height: 32px;
  text-transform: capitalize;
`;

export const Subtitle = styled(Text)`
  margin-bottom: 32px;
  margin-top: 8px;
  color: #f9f9f9;
  text-align: center;
  font-size: 16px;
  font-style: normal;
  opacity: 0.8;
  line-height: 24px;
  padding: 0 104px;
`;

export const CenterThis = styled.div`
  display: flex;
  justify-content: center;
  align-items: center;
  width: 100%;
  margin-bottom: 32px;
`;
