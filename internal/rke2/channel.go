package rke2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Asking RKE2 what "current" means.
//
// Any version written into this tool goes stale the day upstream releases --
// a wizard default of v1.34.5 was found suggesting a version three minors old.
// RKE2 runs a channel server for exactly this question, and it is the same
// authority the install script itself consults; notably its `stable` lags its
// newest release, which is a judgement no hardcoded string carries.

// channelsURL lists every channel with the release it currently names.
const channelsURL = "https://update.rke2.io/v1-release/channels"

// Channels is what the channel server answered.
type Channels struct {
	// Stable is what upstream recommends for production, and what install.sh
	// defaults to. It lags Latest on purpose: a new minor runs in the field
	// for a while before being promoted.
	Stable string
	// Latest is the newest release.
	Latest string
}

// FetchChannels asks the channel server for both answers in one request.
//
// A short timeout and a plain error: this runs where an operator is waiting,
// and on an air-gapped or proxied site the answer is "no answer" -- which the
// caller turns into an empty field the operator fills, not into a guess.
func FetchChannels(ctx context.Context) (Channels, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, channelsURL, nil)
	if err != nil {
		return Channels{}, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return Channels{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Channels{}, fmt.Errorf("rke2: the channel server answered %d", res.StatusCode)
	}

	var body struct {
		Data []struct {
			ID     string `json:"id"`
			Latest string `json:"latest"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return Channels{}, fmt.Errorf("rke2: the channel answer is not what it was: %w", err)
	}

	var ch Channels
	for _, c := range body.Data {
		if !strings.HasPrefix(c.Latest, "v") || !strings.Contains(c.Latest, "+rke2r") {
			continue
		}
		switch c.ID {
		case "stable":
			ch.Stable = c.Latest
		case "latest":
			ch.Latest = c.Latest
		}
	}
	if ch.Stable == "" {
		return Channels{}, fmt.Errorf("rke2: the channel server named no stable release")
	}
	return ch, nil
}
