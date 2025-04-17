export const GET_ACTIONS = `
  query GetActions {
    computePlatform {
      actions {
        id
        type
        spec
        conditions {
          status
          type
          reason
          message
          lastTransitionTime
        }
      }
    }
  }
`
