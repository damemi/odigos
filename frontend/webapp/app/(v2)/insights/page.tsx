'use client';

import React from 'react';
import { InsightsV2 } from '@odigos/ui-kit/containers';

// The rebuilt Insights (PLAT-1678) replaces the legacy page. With insights off, the page itself says so and how to turn them on.
export default function Page() {
  return <InsightsV2 />;
}
