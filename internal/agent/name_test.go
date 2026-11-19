package agent

import "testing"

func TestSanitizeHostname(t *testing.T) {
	cases := map[string]string{
		"My-PC":             "my-pc",
		"DESKTOP-ABC.local": "desktop-abc.local",
		"Foo Bar":           "foo-bar",
		"  Hello_World  ":   "hello_world",
		"---weird---":       "weird",
		"name.with...dots":  "name.with...dots",
	}
	for in, want := range cases {
		if got := SanitizeHostname(in); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
	if SanitizeHostname("CONNECT") != "connect" {
		t.Fatal(SanitizeHostname("CONNECT"))
	}
}

func TestDefaultAgentName(t *testing.T) {
	name, err := DefaultAgentName()
	if err != nil {
		t.Fatal(err)
	}
	if name == "" || name == "connect" || name != SanitizeHostname(name) {
		t.Fatal(name)
	}
}
