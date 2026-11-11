// Package redact removes credential-shaped material from free-form text.
//
// It is its own package because two independent layers need it and neither
// should depend on the other: the application layer scrubs controller-authored
// text on its way to an MCP client, and the logging handler scrubs every value
// on its way to the audit log.
//
// This is a best-effort scrubber over text this server does not control, not
// the security guarantee. The guarantee is structural and lives elsewhere: the
// server never reads Secret objects, and the Kubernetes mappers drop the
// tlsCredentials field so no Secret name is ever carried. This pass exists for
// the remaining case — a Skupper controller, or a client library error, writing
// credential material into a status message.
package redact

import (
	"regexp"
	"strings"
)

// Placeholder replaces any value judged to be credential material.
const Placeholder = "[REDACTED]"

// sensitiveKeys are attribute names whose values never appear in a log record,
// and words that mark a token as credential material wherever they appear.
// Matched case-insensitively as a substring, so "tlsCredentials",
// "bearer_token" and "CA_CERT" are all caught.
var sensitiveKeys = []string{
	"token", "secret", "password", "passwd", "credential", "authorization",
	"bearer", "apikey", "api_key", "privatekey", "private_key", "cert", "ca",
	"tls", "kubeconfig", "jwt", "signature", "cookie",
}

// sensitiveWords is the subset of sensitiveKeys specific enough to redact a
// word by. "ca" and "tls" are excluded: they appear in ordinary hostnames and
// resource names, and redacting on them would destroy useful diagnostics
// without meaningfully improving safety.
var sensitiveWords = []string{
	"token", "secret", "password", "passwd", "credential",
	"apikey", "api_key", "privatekey", "private_key", "jwt",
}

var (
	// pemBlock matches a PEM armour header and everything after it on the same
	// logical run, which is where key material lives.
	pemBlock = regexp.MustCompile(`-----BEGIN [^-]*-----[\s\S]*?(-----END [^-]*-----|$)`)
	// jwtLike matches the three-part base64url shape of a JSON Web Token.
	jwtLike = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_.-]+`)
	// bearerToken matches an Authorization header value echoed into text.
	bearerToken = regexp.MustCompile(`(?i)\bbearer\s+\S+`)
	// opaqueToken matches a long run of token characters.
	opaqueToken = regexp.MustCompile(`\b[A-Za-z0-9+/=_-]{20,}\b`)
	hasDigit    = regexp.MustCompile(`[0-9]`)
	hasLetter   = regexp.MustCompile(`[A-Za-z]`)
)

// IsSensitiveKey reports whether an attribute name should have its value
// replaced outright.
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, k := range sensitiveKeys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// String replaces credential-shaped substrings, leaving the surrounding text
// intact so a status message stays diagnostically useful.
func String(s string) string {
	if s == "" {
		return s
	}
	s = pemBlock.ReplaceAllString(s, Placeholder)
	s = bearerToken.ReplaceAllString(s, "Bearer "+Placeholder)
	s = jwtLike.ReplaceAllString(s, Placeholder)
	s = opaqueToken.ReplaceAllStringFunc(s, func(token string) string {
		if isCredentialLike(token) {
			return Placeholder
		}
		return token
	})
	return redactSensitiveWords(s)
}

// isCredentialLike reports whether a long token looks like opaque credential
// material rather than a Kubernetes name or identifier.
func isCredentialLike(token string) bool {
	// Kubernetes names, pod suffixes and UUIDs are hyphenated or underscored;
	// base64, hex and vendor access keys are not. Requiring no separator at all
	// costs coverage of URL-safe base64 that happens to contain a hyphen, and
	// buys not redacting resource names an operator needs to see. A hyphenated
	// secret is still caught when it names itself (sensitiveWords), when it is
	// a JWT, or when it follows a Bearer scheme.
	if strings.ContainsAny(token, "-_") {
		return false
	}
	// Mixed letters and digits with no word separators is the shape of a
	// base64 or hex secret.
	return hasDigit.MatchString(token) && hasLetter.MatchString(token)
}

// redactSensitiveWords replaces any whitespace-delimited word that names itself
// as credential material.
func redactSensitiveWords(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return s
	}
	changed := false
	for i, field := range fields {
		lower := strings.ToLower(field)
		for _, word := range sensitiveWords {
			if strings.Contains(lower, word) && field != Placeholder {
				fields[i] = Placeholder
				changed = true
				break
			}
		}
	}
	if !changed {
		return s
	}
	return strings.Join(fields, " ")
}
