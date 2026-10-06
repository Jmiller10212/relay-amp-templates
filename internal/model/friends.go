package model

import "time"

type FriendRequest struct {
	ID        string     `json:"id"`
	Requester PublicUser `json:"requester"`
	Recipient PublicUser `json:"recipient"`
	CreatedAt time.Time  `json:"createdAt"`
}

type Friend struct {
	PublicUser
	Online bool `json:"online"`
}

type UserLookup struct {
	PublicUser
	Relationship string `json:"relationship"`
	RequestID    string `json:"requestId,omitempty"`
}
