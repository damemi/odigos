'use client';

import React, { type PropsWithChildren, useEffect } from 'react';
import { useTheme } from 'styled-components';
import { useDarkMode } from '@odigos/ui-kit/store';

// TODO: remove this file after migration to v2

// V1 design has light-mode which is not yet supported in V2 design.
// This is a temporary hack to make sure the V2 design uses dark mode and the correct bg-color...

function Layout({ children }: PropsWithChildren) {
  const theme = useTheme();
  const { darkMode, setDarkMode } = useDarkMode();

  useEffect(() => {
    if (!darkMode) setDarkMode(true);
    document.body.style.backgroundColor = theme.v2.colors.black['500'];
  }, [theme]);

  return children;
}

export default Layout;
