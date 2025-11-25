
.PHONY: generate-token
generate-token:
	PRIVATE_KEY=$$(aws ssm get-parameter --region us-east-1 --name onprem-jwts-private-key --with-decryption --query Parameter.Value --output text) go run .
