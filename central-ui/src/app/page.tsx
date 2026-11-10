'use client';

import { useEffect } from 'react';
import { useRouter } from 'next/navigation';
import { ROUTES } from '@/utils';

export default function App() {
  const router = useRouter();

  useEffect(() => {
    router.push(ROUTES.SIGN_IN);
  }, []);

  return null;
}
