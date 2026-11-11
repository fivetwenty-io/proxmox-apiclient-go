package http

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Fields that stand in for a body, or for the part of it a log line leaves out.
const (
	logFieldBody            = "resp_body"
	logFieldBodyLength      = "resp_body_length"
	logFieldBodyContentType = "resp_body_content_type"

	// truncatedSuffix marks a logged body cut at MaxBodyBytes.
	truncatedSuffix = "...(truncated)"

	// tokenCreateMarker is the key that identifies the response to a token
	// create call, whose "value" member is the token secret.
	tokenCreateMarker = "full-tokenid"
	tokenSecretMember = "value"
)

// alwaysRedactedHeaders are the headers whose values never reach a log line,
// whatever the configured list says. They carry credentials in either
// direction.
func alwaysRedactedHeaders() []string {
	return []string{headerAuthorization, "proxy-authorization", headerCookie, "set-cookie", headerCSRF}
}

// alwaysRedactedParams are the query parameter and JSON member names whose
// values never reach a log line, whatever the configured list says.
func alwaysRedactedParams() []string {
	return []string{
		redactParamPassword, "token", "secret", "ticket", "vncticket", headerCSRF, "otp",
		"privatekey", "private-key", headerAuthorization, headerCookie,
	}
}

// nameSet returns the lower-cased names in the lists as a set, so a
// comparison ignores case on both sides.
func nameSet(lists ...[]string) map[string]struct{} {
	set := make(map[string]struct{})

	for _, list := range lists {
		for _, name := range list {
			set[strings.ToLower(name)] = struct{}{}
		}
	}

	return set
}

// redactedURL renders target for a log line or a hook. It keeps the scheme, host,
// and path, drops the user information and the fragment, and, when the
// configuration logs query parameters, keeps the query with the value of every
// redacted parameter replaced. It is what the query field shows, in the form of
// the URL, so the two never disagree.
func redactedURL(target *url.URL, cfg LogConfig) string {
	if target == nil {
		return ""
	}

	out := url.URL{Scheme: target.Scheme, Host: target.Host, Path: target.Path, RawPath: target.RawPath}

	if cfg.LogQueryParams {
		out.RawQuery = redactRawQuery(target.RawQuery, nameSet(cfg.RedactParams, alwaysRedactedParams()))
	}

	return out.String()
}

// redactRawQuery replaces the value of each parameter whose decoded name is in
// names. A parameter whose name does not decode is redacted too, because
// nothing can show it is harmless. The order and the spelling of the rest are
// kept.
func redactRawQuery(raw string, names map[string]struct{}) string {
	if raw == "" {
		return ""
	}

	pairs := strings.Split(raw, "&")

	for i, pair := range pairs {
		name, _, hasValue := strings.Cut(pair, "=")
		if !hasValue {
			continue
		}

		decoded, err := url.QueryUnescape(name)
		if err != nil {
			pairs[i] = "REDACTED_NAME=" + redactedValue

			continue
		}

		if _, found := names[strings.ToLower(decoded)]; found {
			pairs[i] = name + "=" + url.QueryEscape(redactedValue)
		}
	}

	return strings.Join(pairs, "&")
}

// bodyFields returns the log fields for a response body. A body that parses as
// JSON is logged with the value of every secret member replaced, cut at limit
// bytes when limit is positive. A body that does not parse is not logged: its
// length and content type stand in for it, because text that is not JSON gives
// nothing to tell a secret from the rest.
func bodyFields(body []byte, contentType string, cfg LogConfig) map[string]interface{} {
	redacted, ok := redactJSONBody(body, nameSet(cfg.RedactParams, cfg.RedactHeaders, alwaysRedactedParams()))
	if !ok {
		return map[string]interface{}{
			logFieldBodyLength:      len(body),
			logFieldBodyContentType: contentType,
		}
	}

	if cfg.MaxBodyBytes > 0 && len(redacted) > cfg.MaxBodyBytes {
		cut := cfg.MaxBodyBytes
		for cut > 0 && !utf8.RuneStart(redacted[cut]) {
			cut--
		}

		redacted = append(redacted[:cut:cut], truncatedSuffix...)
	}

	return map[string]interface{}{logFieldBody: string(redacted)}
}

// redactJSONBody parses body as one JSON value and returns it re-encoded with
// the value of each member named in names replaced. It reports false when body
// is not exactly one JSON value.
func redactJSONBody(body []byte, names map[string]struct{}) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var value interface{}

	err := decoder.Decode(&value)
	if err != nil {
		return nil, false
	}

	// Anything after the first value makes the body not one JSON document.
	if decoder.More() {
		return nil, false
	}

	var out bytes.Buffer

	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)

	err = encoder.Encode(redactJSONValue(value, names))
	if err != nil {
		return nil, false
	}

	return bytes.TrimRight(out.Bytes(), "\n"), true
}

// redactJSONValue returns value with the value of each object member named in
// names replaced, at any depth. In an object that carries the member a token
// create response uses to name the new token, the "value" member is the token
// secret and is replaced as well.
func redactJSONValue(value interface{}, names map[string]struct{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		_, isTokenCreate := lookupFold(typed, tokenCreateMarker)
		out := make(map[string]interface{}, len(typed))

		for key, member := range typed {
			lower := strings.ToLower(key)
			_, secret := names[lower]

			if secret || (isTokenCreate && lower == tokenSecretMember) {
				out[key] = redactedValue

				continue
			}

			out[key] = redactJSONValue(member, names)
		}

		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, member := range typed {
			out[i] = redactJSONValue(member, names)
		}

		return out
	default:
		return value
	}
}

// lookupFold finds key in object ignoring case.
func lookupFold(object map[string]interface{}, key string) (interface{}, bool) {
	for name, member := range object {
		if strings.EqualFold(name, key) {
			return member, true
		}
	}

	return nil, false
}
