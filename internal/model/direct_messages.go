package model

import "time"

type DirectConversation struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	CreatedAt   time.Time  `json:"createdAt"`
	Peer        PublicUser `json:"peer"`
	Online      *bool      `json:"online,omitempty"`
	CanSend     bool       `json:"canSend"`
	LastMessage *Message   `json:"lastMessage,omitempty"`
	UnreadCount int        `json:"unreadCount"`
}

type ReadState struct {
	ConversationID    string    `json:"conversationId"`
	LastReadMessageID int64     `json:"lastReadMessageId"`
	UnreadCount       int       `json:"unreadCount"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type DirectAccess struct {
	ConversationID string
	PeerID         string
	CanSend        bool
}
