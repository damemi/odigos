'use client';

import { useSessionRedirects } from '@/hooks';
import styled from 'styled-components';
import { CenterThis, FadeLoader } from '@odigos/ui-kit/components';

const Container = styled(CenterThis)`
  height: 100vh;
`;

export default function App() {
  useSessionRedirects();

  return (
    <Container>
      <FadeLoader scale={2} />
    </Container>
  );
}
