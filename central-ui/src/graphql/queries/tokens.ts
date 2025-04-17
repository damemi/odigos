export const GET_TOKENS = `
  query GetTokens {
    computePlatform {
      apiTokens {
        token
        name
        issuedAt
        expiresAt
      }
    }
  }
`
