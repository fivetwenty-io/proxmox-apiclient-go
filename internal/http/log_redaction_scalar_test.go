package http //nolint:testpackage // white-box test: reads the log fields of the redaction helpers

import (
	"strings"
	"testing"
	"unicode"
)

// TestLoggedBodyWithAStringResultIsNotLogged checks that a body whose top level
// is a string, and a body whose "data" member is a string, do not reach the log
// with that string, because nothing names it as harmless.
func TestLoggedBodyWithAStringResultIsNotLogged(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"bare string":     `"SCALARSECRET"`,
		"data string":     `{"data":"DATASTRSECRET"}`,
		"upper data":      `{"DATA":"DATASTRSECRET","success":1}`,
		"bare number":     `12345`,
		"bare null":       `null`,
		"bare true":       `true`,
		"data with extra": `{"success":1,"data":"DATASTRSECRET","message":"ok"}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fields := bodyFields([]byte(body), "application/json", LogConfig{})

			for key, value := range fields {
				text, _ := value.(string)
				if strings.Contains(text, "SECRET") || strings.Contains(key, "SECRET") {
					t.Errorf("field %q = %q holds the string result", key, text)
				}
			}

			if strings.HasPrefix(body, `{`) {
				if _, logged := fields[logFieldBody]; !logged {
					t.Errorf("an object body was not logged: %v", fields)
				}

				return
			}

			if _, logged := fields[logFieldBodyLength]; !logged {
				t.Errorf("a scalar body has no length field: %v", fields)
			}
		})
	}
}

// TestLoggedBodyEscapesEveryStringItEmits puts control and direction characters
// in a string at each place a body can hold one, and checks that none reaches
// the log field raw.
func TestLoggedBodyEscapesEveryStringItEmits(t *testing.T) {
	t.Parallel()

	const hostile = `a\u202eb\u001b[2J\u2066c\u200ed\u0007e\u2028f`

	bodies := map[string]string{
		"member":        `{"note":"` + hostile + `"}`,
		"nested member": `{"data":{"inner":{"note":"` + hostile + `"}}}`,
		"array item":    `{"data":["` + hostile + `"]}`,
		"top array":     `["` + hostile + `"]`,
		"member name":   `{"data":{"` + hostile + `":"x"}}`,
		"data array":    `{"data":[{"note":"` + hostile + `"}]}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, _ := bodyFields([]byte(body), "application/json", LogConfig{})[logFieldBody].(string)
			if got == "" {
				t.Fatal("the body was not logged")
			}

			for _, r := range got {
				if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
					t.Errorf("logged body %q holds the raw character %U", got, r)
				}
			}
		})
	}
}
