export const CREATE_INSTRUMENTATION_RULE = `
  mutation CreateInstrumentationRule($instrumentationRule: InstrumentationRuleInput!) {
    createInstrumentationRule(instrumentationRule: $instrumentationRule) {
      type
      ruleId
      ruleName
      notes
      disabled
      mutable
      profileName
      payloadCollection {
        httpRequest {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        httpResponse {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        dbQuery {
          maxPayloadLength
          dropPartialPayloads
        }
        messaging {
          maxPayloadLength
          dropPartialPayloads
        }
      }
      codeAttributes {
        column
        filePath
        function
        lineNumber
        namespace
        stacktrace
      }
      headersCollection {
        headerKeys
      }
    }
  }
`;

export const UPDATE_INSTURMENTATION_RULE = `
  mutation UpdateInstrumentationRule($ruleId: ID!, $instrumentationRule: InstrumentationRuleInput!) {
    updateInstrumentationRule(ruleId: $ruleId, instrumentationRule: $instrumentationRule) {
      type
      ruleId
      ruleName
      notes
      disabled
      mutable
      profileName
      payloadCollection {
        httpRequest {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        httpResponse {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        dbQuery {
          maxPayloadLength
          dropPartialPayloads
        }
        messaging {
          maxPayloadLength
          dropPartialPayloads
        }
      }
      codeAttributes {
        column
        filePath
        function
        lineNumber
        namespace
        stacktrace
      }
      headersCollection {
        headerKeys
      }
    }
  }
`;

export const DELETE_INSTURMENTATION_RULE = `
  mutation DeleteInstrumentationRule($ruleId: ID!) {
    deleteInstrumentationRule(ruleId: $ruleId)
  }
`;
