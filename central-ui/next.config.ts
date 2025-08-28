import type { NextConfig } from 'next';

const withBundleAnalyzer = require('@next/bundle-analyzer')({
  enabled: process.env.ANALYZE === 'true',
});

const nextConfig: NextConfig = {
  reactStrictMode: false,
  images: {
    unoptimized: true,
  },
  compiler: {
    styledComponents: true,
    removeConsole: process.env.NODE_ENV === 'production', // Removes console.logs in production
  },
  experimental: {
    optimizePackageImports: ['@apollo/client', 'graphql', 'zustand'], // Tree shaking for specific packages
  },
  webpack: (config, { isServer }) => {
    // Only apply client-side optimizations
    if (!isServer) {
      // Enable tree shaking and side effects optimization
      config.optimization = {
        ...config.optimization,
        usedExports: true, // Enable tree shaking
        sideEffects: true, // Enable side effects optimization
        concatenateModules: true, // Module concatenation
        mergeDuplicateChunks: true, // Scope hoisting
        splitChunks: {
          chunks: 'all',
          minSize: 20000, // Minimum chunk size: 20KB
          maxSize: 244000, // Maximum chunk size: ~244KB
          cacheGroups: {
            vendor: {
              // General vendor code
              test: /[\\/]node_modules[\\/]/,
              name: 'vendors',
              priority: 10,
              enforce: true,
            },
            common: {
              // Shared code between pages
              name: 'common',
              minChunks: 2,
              priority: 5,
              reuseExistingChunk: true,
            },
            styledComponents: {
              // Styled-components specific
              test: /[\\/]node_modules[\\/]styled-components[\\/]/,
              name: 'styled-components',
              priority: 20,
              enforce: true,
            },
            apollo: {
              // Apollo Client specific
              test: /[\\/]node_modules[\\/]@apollo[\\/]/,
              name: 'apollo',
              priority: 15,
              enforce: true,
            },
            graphql: {
              // GraphQL specific
              test: /[\\/]node_modules[\\/](graphql|graphql-ws)[\\/]/,
              name: 'graphql',
              priority: 15,
              enforce: true,
            },
            zustand: {
              // Zustand specific
              test: /[\\/]node_modules[\\/]zustand[\\/]/,
              name: 'zustand',
              priority: 15,
              enforce: true,
            },
          },
        },
      };

      // Enhanced minification in production
      if (process.env.NODE_ENV === 'production') {
        const TerserPlugin = require('terser-webpack-plugin');

        config.optimization.minimizer = [
          new TerserPlugin({
            terserOptions: {
              compress: {
                drop_console: true, // Remove console statements
                drop_debugger: true, // Remove debugger statements
                pure_funcs: ['console.log', 'console.info', 'console.debug', 'console.warn'],
              },
              mangle: true, // Variable name mangling
              format: { comments: false }, // Remove comments
            },
            extractComments: false,
          }),
        ];
      }
    }

    return config;
  },
};

module.exports = withBundleAnalyzer(nextConfig);
