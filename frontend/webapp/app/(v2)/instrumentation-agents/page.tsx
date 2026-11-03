'use client';

import React from 'react';
import { InstrumentationAgentsNext } from '@odigos/ui-kit/containers';

// Agents and Go offsets both flow via `useOdigosApi()` inside the kit container.
export default function Page() {
  // PLAT-1604 spike: shadcn version of the agents page (swap back to `InstrumentationAgents` to compare).
  return <InstrumentationAgentsNext />;
}
