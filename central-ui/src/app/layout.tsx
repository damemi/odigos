'use client';

import React, { type PropsWithChildren } from 'react';
import dynamic from 'next/dynamic';
import client from '@/lib/apollo-client';
import { ApolloProvider } from '@apollo/client';
import { ToastList } from '@odigos/ui-kit/containers';

const ThemeProvider = dynamic(() => import('@/lib/theme-provider'), { ssr: false });
const ProtectedRoute = dynamic(() => import('@/components/auth/protected-route'), { ssr: false });

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
          <ToastList />
          <ApolloProvider client={client}>
            <ProtectedRoute>{children}</ProtectedRoute>
          </ApolloProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}

export default RootLayout;
