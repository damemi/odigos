# onprem-token-generator

For on-premises installations of odigos, we cannot verify a token by calling odigos cloud API.

Instead, we generate a JWT token, give it to the customer to use for installing odigos, and then verify it in odiglet enterprise.

This repo is used to create such tokens for on-prem customers. The process is currently manual, but we can automate it in the future.

## Usage

With aws-cli profile that can access aws SSM:

```bash
JWT_AUDIENCE=foobar make generate-token
```

### Environment variables

- PRIVATE_KEY: the private key to sign the token with. You can find it in aws SSM parameter store [here](https://us-east-1.console.aws.amazon.com/systems-manager/parameters/onprem-jwts-private-key/description?region=us-east-1&tab=Table).

- JWT_AUDIENCE: the audience of the token, e.g. the name of the organization for which the token was issued. This is required. Can be any string that is meaningful to us to identify the customer.

- TOKEN_EXPIRY_DAYS: the number of days the token is valid for. Defaults to 365 (1 year).

- ODIGOS_PROFILES (optional): Specifies client profiles as a comma-separated list. Each profile defines unique custom resources that will be applied within the client cluster.
Example: ODIGOS_PROFILES=kratos,code-attributes

### Verify the token

After running the above command, you will get a token printed to stdout. like so:

```bash
2023/12/13 12:05:15 Token: eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJhdWQiOiJmb29iYXIiLCJleHAiOjE3MzM5OTc5MTUsImlhdCI6MTcwMjQ2MTkxNSwiaXNzIjoiaHR0cHM6Ly9vZGlnb3MuaW8iLCJzdWIiOiJodHRwczovL29kaWdvcy5pby9vbnByZW0ifQ.JSPl5d3mbB9xA5hRH4P5bj5BOrGa14n0U7F8vttg9ChnJ3wtWz9VDiAOZTuLGj3G0lhQ5XzIeV75RDUYep_UfP9sPrfWaDLkf_YIpEb3IxPYbYCLsc6vD70MeDsK_os1T-ECg1rntlUlY7QjgAMH3H-WrOcc0y4yuwoRFLQLvq866ypS-F1MoPvo4Z1gPjiqwE6W0Ox7m_7c2URq25BFdIVbUTtLwSYHmlPg1JmVCs0V9BvnW2mH_2_ulQwwlqSPazVAOM9uZ25_oM5IfsFxghCTJLmCkvZOPmUl2Th1fc8Mozw25y2VJBXk0dRtvKkGrKB6klaervrpZsEeoW0KjQ
```

You can verify it in [jwt.io](https://jwt.io/)

### Give it to the customer

Send the token to the customer and ask them to use it for installing odigos onprem. Set a reminder to yourself to create a new token for the customer before the expiry date.
