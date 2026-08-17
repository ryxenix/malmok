package rke2

import (
	"context"
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

// channelURL answers with a redirect to the release the channel names.
const channelURL = "https://update.rke2.io/v1-release/channels/stable"

// StableVersion asks the channel server for the current stable release.
//
// A short timeout and a plain error: this runs where an operator is waiting,
// and on an air-gapped or proxied site the answer is "no answer" -- which the
// caller turns into an empty field the operator fills, not into a guess.
func StableVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, channelURL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{
		// The version is in the redirect's Location; following it would
		// download a GitHub release page nobody wants.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	loc := res.Header.Get("Location")
	i := strings.LastIndex(loc, "/")
	if res.StatusCode/100 != 3 || i < 0 || i == len(loc)-1 {
		return "", fmt.Errorf("rke2: the channel server answered %d with location %q", res.StatusCode, loc)
	}
	v := loc[i+1:]
	if !strings.HasPrefix(v, "v") || !strings.Contains(v, "+rke2r") {
		return "", fmt.Errorf("rke2: %q does not look like an RKE2 version", v)
	}
	return v, nil
}
