package api

import "time"

// SetzeFeedGrenzenFuerTest setzt beide Drosseln des iCal-Feeds (je Abo, je
// Adresse bei unbekanntem Token) auf max Abrufe je Stunde (F-2109).
func (a *ICalAPI) SetzeFeedGrenzenFuerTest(max int) {
	a.feedLimit = newRateLimiter(max, time.Hour)
	a.fehlLimit = newRateLimiter(max, time.Hour)
}
