'use client';

import React, { type PropsWithChildren } from 'react';
import dynamic from 'next/dynamic';
import { ROUTES } from '@/utils';
import { usePathname } from 'next/navigation';
import { ToastList, ToastListNext } from '@odigos/ui-kit/containers';
import OdigosApiAdapter from '@/lib/odigos-api-adapter';

const ThemeProvider = dynamic(() => import('@/lib/theme-provider'), { ssr: false });
const ProtectedRoute = dynamic(() => import('@/components/auth/protected-route'), { ssr: false });

// Pages without the app's chrome (sign-in, password update) keep the legacy toasts; the rest use the redesigned ones.
const LEGACY_TOAST_PATHS: string[] = [ROUTES.ROOT, ROUTES.SIGN_IN, ROUTES.UPDATE_YOUR_PASSWORD];

const Toasts = () => {
  const pathname = usePathname();

  return LEGACY_TOAST_PATHS.includes(pathname) ? <ToastList /> : <ToastListNext />;
};

function RootLayout({ children }: PropsWithChildren) {
  return (
    <html lang='en'>
      <head>
        <link rel='manifest' href='/manifest.json' />
        <link rel='icon' href='/favicon.svg' type='image/svg' />
        <meta name='description' content='Odigos Central' />
        <title>Odigos Central</title>
      </head>

      <body
        suppressHydrationWarning={true}
        style={{
          width: '100vw',
          minHeight: '100vh',
          margin: 0,
          backgroundColor: '#151618',
        }}
      >
        <ThemeProvider>
          <Toasts />
          <OdigosApiAdapter>
            <ProtectedRoute>{children}</ProtectedRoute>
          </OdigosApiAdapter>
        </ThemeProvider>
      </body>
    </html>
  );
}

export default RootLayout;
