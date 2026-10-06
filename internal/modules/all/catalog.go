package all

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"relay/internal/accounts"
	"relay/internal/chat"
	"relay/internal/clientapi"
	"relay/internal/config"
	"relay/internal/directmessages"
	"relay/internal/friends"
	"relay/internal/health"
	"relay/internal/model"
	"relay/internal/module"
	"relay/internal/persistence"
	"relay/internal/realtime"
	"relay/internal/servers"
)

type Catalog struct {
	Registry       *module.Registry
	Persistence    *persistence.Module
	Accounts       *accounts.Module
	Chat           *chat.Module
	Realtime       *realtime.Module
	ClientAPI      *clientapi.Module
	Friends        *friends.Module
	DirectMessages *directmessages.Module
	Servers        *servers.Module
	Ready          atomic.Bool
}

func Build(cfg config.Config, logger *log.Logger) (*Catalog, error) {
	p := persistence.New(cfg.DataDir, cfg.MaxStoredMessages)
	a := accounts.New(accounts.NewSupabaseAuthProvider(cfg.SupabaseURL, cfg.SupabasePublishableKey), p, accounts.Config{
		RegistrationEnabled: cfg.Authentication.RegistrationEnabled, PublicBaseURL: cfg.Authentication.PublicBaseURL, CookieSecureMode: cfg.Authentication.CookieSecureMode,
		UsernameMin: cfg.Authentication.UsernameMinRunes, UsernameMax: cfg.Authentication.UsernameMaxRunes, DisplayNameMin: cfg.Authentication.DisplayNameMinRunes, DisplayNameMax: cfg.Authentication.DisplayNameMaxRunes,
		UsernameCooldown: time.Duration(cfg.Authentication.UsernameCooldownHours) * time.Hour, ReservationLifetime: time.Duration(cfg.Authentication.ReservationLifetimeHours) * time.Hour,
		RecoveryGrantLifetime: time.Duration(cfg.Authentication.RecoveryGrantMinutes) * time.Minute, RateLimitAttempts: cfg.Authentication.RateLimitAttempts,
		RateLimitWindow: time.Duration(cfg.Authentication.RateLimitWindowSeconds) * time.Second, SessionValidation: time.Duration(cfg.Authentication.SessionValidationSeconds) * time.Second,
	}, logger)
	c := chat.New(p, a, chat.Config{
		HistoryLimit: cfg.HistoryLimit, MessageMaxRunes: cfg.MessageMaxRunes,
		MaxConnections: cfg.MaxConnections, RoomName: cfg.RoomName, SystemName: cfg.SystemName,
		WelcomeMessage: cfg.WelcomeMessage,
		Rate:           cfg.RateLimitPerSecond, Burst: cfg.RateLimitBurst,
		PingInterval: time.Duration(cfg.WebSocketPingSeconds) * time.Second, SessionValidation: time.Duration(cfg.Authentication.SessionValidationSeconds) * time.Second,
		Events: chat.EventConfig{AnnounceJoins: cfg.Events.AnnounceJoins, AnnounceLeaves: cfg.Events.AnnounceLeaves, AnnounceServerStart: cfg.Events.AnnounceServerStart, AnnounceServerStop: cfg.Events.AnnounceServerStop, Persist: cfg.Events.Persist},
	}, logger)
	rt := realtime.New(a, realtime.Config{MaxConnections: cfg.MaxConnections, PingInterval: time.Duration(cfg.WebSocketPingSeconds) * time.Second, SessionValidation: time.Duration(cfg.Authentication.SessionValidationSeconds) * time.Second}, logger)
	f := friends.New(a, p, rt, friends.Config{UsernameMin: cfg.Authentication.UsernameMinRunes, UsernameMax: cfg.Authentication.UsernameMaxRunes, LookupsPerMinute: cfg.UserLookupsPerMinute, MutationsPerMinute: cfg.FriendMutationsPerMinute})
	dm := directmessages.New(a, p, rt)
	sv := servers.New(a, p, rt, servers.Config{NameMax: cfg.ServerNameMaxRunes, MaxOwned: cfg.MaxOwnedServers, MaxMemberships: cfg.MaxServerMemberships, InvitesPerHour: cfg.ServerInvitesPerHour})
	client := clientapi.New(a, p, rt, c, clientapi.Config{RoomName: cfg.RoomName, HistoryLimit: cfg.HistoryLimit, MessageMaxRunes: cfg.MessageMaxRunes, Rate: cfg.RateLimitPerSecond, Burst: cfg.RateLimitBurst, FriendsEnabled: cfg.Modules.Friends, DirectMessagesEnabled: cfg.Modules.DirectMessages, ServersEnabled: cfg.Modules.Servers})
	if cfg.Modules.DirectMessages {
		client.SetDirectMessages(dm)
	}
	if cfg.Modules.Servers {
		client.SetServers(sv)
	}
	cat := &Catalog{Persistence: p, Accounts: a, Chat: c, Realtime: rt, ClientAPI: client, Friends: f, DirectMessages: dm, Servers: sv}
	a.OnProfileChanged(func(user model.PublicUser) { c.ProfileUpdated(user); rt.ProfileUpdated(user) })
	c.OnMessagePublished(func(message model.Message) { rt.PublishAll("message.created", map[string]any{"message": message}) })
	h := health.New(cat.Ready.Load, p.Healthy, rt.UserCount, func() []string {
		if cat.Registry == nil {
			return nil
		}
		return cat.Registry.Names()
	})
	r, err := module.New(p, a, rt, c, client, f, dm, sv, h)
	if err != nil {
		return nil, err
	}
	cat.Registry = r
	enabled := []string{}
	if cfg.Modules.Persistence {
		enabled = append(enabled, "persistence")
	}
	if cfg.Modules.Accounts {
		enabled = append(enabled, "accounts")
	}
	if cfg.Modules.Realtime {
		enabled = append(enabled, "realtime")
	}
	if cfg.Modules.Friends {
		enabled = append(enabled, "friends")
	}
	if cfg.Modules.DirectMessages {
		enabled = append(enabled, "direct_messages")
	}
	if cfg.Modules.Servers {
		enabled = append(enabled, "servers")
	}
	if cfg.Modules.Chat {
		enabled = append(enabled, "chat", "client_api")
	}
	if cfg.Modules.Health {
		enabled = append(enabled, "health")
	}
	if err := r.Enable(enabled); err != nil {
		return nil, err
	}
	return cat, nil
}

func (c *Catalog) Healthy(ctx context.Context) bool { return c.Persistence.Healthy(ctx) }
