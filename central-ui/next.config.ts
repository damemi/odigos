import type { NextConfig } from 'next';
import bundleAnalyzer from '@next/bundle-analyzer';

// Bundle analyzer configuration
const withBundleAnalyzer = bundleAnalyzer({
  enabled: process.env.ANALYZE === 'true',
});

const CENTRAL_BACKEND_URL = process.env.CENTRAL_BACKEND_URL || 'http://central-backend:8081';

const nextConfig: NextConfig = {
  reactStrictMode: false,
  images: {
    unoptimized: true,
  },
  compiler: {
    styledComponents: true,
    // Remove console.logs in production
    removeConsole: process.env.NODE_ENV === 'production',
  },
  // Enable compression
  compress: true,
  // Enable source maps only in development
  productionBrowserSourceMaps: false,
  // Enable experimental optimizations
  experimental: {
    // Enable tree shaking for better bundle optimization
    optimizePackageImports: ['@apollo/client', '@odigos/ui-kit', 'graphql', 'react', 'react-dom', 'rxjs', 'styled-components', 'zustand'],
  },
  // Turbopack configuration — pin shared deps so yalc'd ui-kit resolves the
  // host app's copies instead of nested duplicates.
  turbopack: {
    resolveAlias: {
      'styled-components': './node_modules/styled-components',
      zustand: './node_modules/zustand',
    },
  },
  async rewrites() {
    return [
      { source: '/diagnose/download', destination: `${CENTRAL_BACKEND_URL}/diagnose/download` },
      { source: '/sse/:path*', destination: `${CENTRAL_BACKEND_URL}/sse/:path*` },
      { source: '/api/signin', destination: `${CENTRAL_BACKEND_URL}/api/signin` },
      { source: '/api/signout', destination: `${CENTRAL_BACKEND_URL}/api/signout` },
      { source: '/api/auth/:path*', destination: `${CENTRAL_BACKEND_URL}/api/auth/:path*` },
      { source: '/realms/:path*', destination: `${CENTRAL_BACKEND_URL}/realms/:path*` },
      { source: '/resources/:path*', destination: `${CENTRAL_BACKEND_URL}/resources/:path*` },
    ];
  },
};

export default withBundleAnalyzer(nextConfig);
