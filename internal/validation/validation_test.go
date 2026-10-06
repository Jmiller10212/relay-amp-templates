package validation

import "testing"

func TestPlainText(t *testing.T) {
	v, err := PlainText("  hello  ", "message", 10)
	if err != nil || v != "hello" {
		t.Fatalf("%q %v", v, err)
	}
	for _, bad := range []string{"", "a\nb", "1234"} {
		max := 3
		if bad == "a\nb" {
			max = 10
		}
		if _, err := PlainText(bad, "message", max); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestUsernameAcceptsDocumentedASCIIFormat(t *testing.T) {
	for _, value := range []string{"jmiller10212", "abc", "relay_user_123"} {
		got, err := Username(value, 3, 32)
		if err != nil || got != value {
			t.Fatalf("Username(%q) = %q, %v", value, got, err)
		}
	}
	for input, want := range map[string]string{
		"Jmiller10212": "jmiller10212",
		"relay​user":   "relayuser",
		" relay_user ": "relay_user",
	} {
		got, err := Username(input, 3, 32)
		if err != nil || got != want {
			t.Fatalf("Username(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, value := range []string{"ab", "relay-user", "relay user", "relay.user"} {
		if _, err := Username(value, 3, 32); err == nil {
			t.Fatalf("Username accepted invalid value %q", value)
		}
	}
}
