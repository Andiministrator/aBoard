package notifications

// adapters.go — kleine Helfer, die die Storage-/Mail-Layer an die im
// Notification-Subsystem definierten Interfaces andocken.

import (
	"context"

	mailpkg "github.com/Andiministrator/aBoard/server/mail"
	"github.com/Andiministrator/aBoard/server/storage"
)

// SMTPConfigFromConfigStore adapter macht aus *storage.ConfigRepo eine
// SMTPConfigSource (lädt SMTP-Config pro Aufruf, kein Cache).
type SMTPConfigFromConfigStore struct {
	Repo *storage.ConfigRepo
}

// LoadSMTP implementiert SMTPConfigSource.
func (s SMTPConfigFromConfigStore) LoadSMTP(ctx context.Context) (mailpkg.Config, error) {
	cfg, err := mailpkg.LoadConfig(ctx, s.Repo)
	if err != nil {
		return mailpkg.Config{}, err
	}
	return cfg, nil
}

// UsersFromRepo macht aus *storage.UserRepo einen UserResolver.
type UsersFromRepo struct {
	Repo *storage.UserRepo
}

// Resolve implementiert UserResolver.
func (u UsersFromRepo) Resolve(ctx context.Context, userID string) (email, name string, isAdmin bool, err error) {
	row, err := u.Repo.GetByID(ctx, userID)
	if err != nil {
		return "", "", false, err
	}
	return row.Email, row.Name, row.IsAdmin, nil
}

// SubscriptionsFromRepo macht aus *storage.NotificationSubscriptionRepo
// einen SubscriberSource (heute identisch — Reservefeld für künftige
// Anreicherung, z. B. Board-Membership-Filter).
type SubscriptionsFromRepo struct {
	Repo *storage.NotificationSubscriptionRepo
}

// Subscribers implementiert SubscriberSource.
func (s SubscriptionsFromRepo) Subscribers(ctx context.Context, eventType, channel string) ([]string, error) {
	return s.Repo.EnabledSubscribers(ctx, eventType, channel)
}
