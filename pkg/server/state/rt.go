package state

import (
	"net/http"
	"time"

	"github.com/Anti-Raid/corelib_go/objectstorage"
)

var rtDefaultExp = 5 * time.Minute

type RoundtripJobDl struct {
	guildId string
	next    http.RoundTripper
}

func NewRoundtripJobDl(guildId string, next http.RoundTripper) *RoundtripJobDl {
	return &RoundtripJobDl{
		guildId: guildId,
		next:    next,
	}
}

func (t RoundtripJobDl) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	// Create presigned url
	expiry := req.URL.Query().Get("exp")

	var expiryDuration time.Duration

	if expiry != "" {
		expiryDuration, err = time.ParseDuration(expiry)

		if err != nil {
			return nil, err
		}
	} else {
		expiryDuration = rtDefaultExp
	}

	url, err := ObjectStorage.GetUrl(
		req.Context(),
		objectstorage.GuildBucket(t.guildId),
		req.URL.Path,
		"",
		expiryDuration,
	)

	if err != nil {
		return nil, err
	}

	req.URL = url
	req.Host = url.Host

	return t.next.RoundTrip(req)
}
