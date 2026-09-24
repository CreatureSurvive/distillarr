package notify

import (
	"errors"
	"net/url"
	"strings"

	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
)

// Shoutrrr is the real SendFunc. Errors are scrubbed of the URL and its
// secret-looking parts, since they end up in logs and the settings UI.
func Shoutrrr(rawURL, title, body string) error {
	r := router.ServiceRouter{Timeout: router.DefaultTimeout}
	svc, err := r.Locate(rawURL)
	if err != nil {
		return scrub(err, rawURL)
	}
	params := types.Params{types.TitleKey: title}
	if err := svc.Send(body, &params); err != nil {
		return scrub(err, rawURL)
	}
	return nil
}

// Scheme returns the service name of a shoutrrr URL ("discord", "ntfy"),
// or "" when it doesn't parse.
func Scheme(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme)
}

// scrub removes rawURL and anything in it that looks like a credential
// (userinfo, query values, long path segments or host labels) from err.
func scrub(err error, rawURL string) error {
	msg := err.Error()
	secrets := []string{rawURL}
	if u, perr := url.Parse(rawURL); perr == nil {
		if u.User != nil {
			secrets = append(secrets, u.User.Username())
			if p, ok := u.User.Password(); ok {
				secrets = append(secrets, p)
			}
		}
		for _, vs := range u.Query() {
			secrets = append(secrets, vs...)
		}
		for _, part := range append(strings.Split(u.Path, "/"), strings.Split(u.Host, ".")...) {
			if len(part) >= 8 {
				secrets = append(secrets, part)
			}
		}
	}
	for _, sec := range secrets {
		if len(sec) >= 4 {
			msg = strings.ReplaceAll(msg, sec, "***")
		}
	}
	return errors.New(msg)
}
