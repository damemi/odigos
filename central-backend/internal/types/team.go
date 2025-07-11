package types

type Team struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
}

type CreateTeamRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

type CreateTeamResult struct {
	Success bool   `json:"success"`
	Team    *Team  `json:"team,omitempty"`
	Error   string `json:"error,omitempty"`
}
