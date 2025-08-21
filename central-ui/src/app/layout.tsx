'use client';

import React, { type PropsWithChildren } from 'react';
import dynamic from 'next/dynamic';
import client from '@/lib/apollo-client';
import { ApolloProvider } from '@apollo/client';
import { useDarkMode } from '@odigos/ui-kit/store';
import { ToastList } from '@odigos/ui-kit/containers';

const ThemeProvider = dynamic(() => import('@/lib/theme-provider'), { ssr: false });

function RootLayout({ children }: PropsWithChildren) {
  const { darkMode } = useDarkMode();

  return (
    <html lang='en'>
      <head>
        <link rel='manifest' href='/manifest.json' />
        <link rel='icon' href='/favicon.svg' type='image/svg' />
        <meta name='description' content='Odigos Tower' />
        <title>Odigos Tower</title>
      </head>

      <body
        suppressHydrationWarning={true}
        style={{
          width: '100vw',
          minHeight: '100vh',
          margin: 0,
          backgroundColor: darkMode ? '#111111' : '#EEEEEE',
        }}
      >
        <ThemeProvider>
          <ToastList />
          <ApolloProvider client={client}>{children}</ApolloProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}

export default RootLayout;
