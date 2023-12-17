
.PHONY: generate-token
generate-token:
	PRIVATE_KEY=$$(aws ssm get-parameter --name onprem-jwts-private-key --with-decryption --query Parameter.Value --output text) go run .
