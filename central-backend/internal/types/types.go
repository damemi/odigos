package types

type GqlRequestPayload struct {
	Query     string                 `json:"query"`
	RequestId string                 `json:"request_id"`
	Variables map[string]interface{} `json:"variables"`
	IsAdmin   bool                   `json:"is_admin"`
}

type ResponsePayload struct {
	ProxyID  string `json:"proxy_id"`
	Response string `json:"response"`
}

type ConnectionStatus struct {
	ProxyID string `json:"proxy_id"`
	Status  string `json:"status"`
}

type UserSummary struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}
