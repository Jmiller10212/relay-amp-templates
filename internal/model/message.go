package model

import "time"

type Message struct {
	ID             int64     `json:"id"`
	ConversationID string    `json:"conversationId"`
	Kind           string    `json:"kind"`
	UserID         string    `json:"userId,omitempty"`
	Username       string    `json:"username"`
	DisplayName    string    `json:"displayName"`
	Text           string    `json:"text"`
	CreatedAt      time.Time `json:"createdAt"`
}

const GlobalConversationID = "00000000-0000-7000-8000-000000000001"

type Conversation struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"createdAt"`
}

type PublicUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

type Profile struct {
	PublicUser
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	UsernameChangedAt *time.Time `json:"usernameChangedAt,omitempty"`
}
