package model

import "time"

type Server struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Role             string          `json:"role"`
	DefaultChannelID string          `json:"defaultChannelId"`
	Channels         []ServerChannel `json:"channels,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

type ServerChannel struct {
	ID             string    `json:"id"`
	ServerID       string    `json:"serverId"`
	ConversationID string    `json:"conversationId"`
	Name           string    `json:"name"`
	Position       int       `json:"position"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type ServerMember struct {
	User     PublicUser `json:"user"`
	Role     string     `json:"role"`
	Online   bool       `json:"online"`
	JoinedAt time.Time  `json:"joinedAt"`
}

type ServerInvite struct {
	ID        string     `json:"id"`
	Server    Server     `json:"server"`
	Inviter   PublicUser `json:"inviter"`
	CreatedAt time.Time  `json:"createdAt"`
}

type ChannelAccess struct {
	ConversationID string
	ServerID       string
	MemberIDs      []string
}

type ServerSearchResult struct {
	Message Message       `json:"message"`
	Channel ServerChannel `json:"channel"`
}

type ChannelPin struct {
	ChannelID string     `json:"channelId"`
	Message   Message    `json:"message"`
	PinnedBy  PublicUser `json:"pinnedBy"`
	PinnedAt  time.Time  `json:"pinnedAt"`
}

type ChannelNotificationPreference struct {
	ChannelID string    `json:"channelId"`
	Mode      string    `json:"mode"`
	UpdatedAt time.Time `json:"updatedAt"`
}
