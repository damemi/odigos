export const CREATE_ACTION = `
  mutation CreateAction($action: ActionInput!) {
    createAction(action: $action) {
      id
      type
    }
  }
`

export const UPDATE_ACTION = `
  mutation UpdateAction($id: ID!, $action: ActionInput!) {
    updateAction(id: $id, action: $action) {
      id
      type
    }
  }
`

export const DELETE_ACTION = `
  mutation DeleteAction($id: ID!, $actionType: String!) {
    deleteAction(id: $id, actionType: $actionType)
  }
`
