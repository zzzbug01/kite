package sshaddr

import "testing"

func TestParseSS(t *testing.T) {
	out := "" +
		"LISTEN 0 128 127.0.0.1:22 0.0.0.0:* users:((\"sshd\",pid=9,fd=3))\n" +
		"LISTEN 0 128 127.0.0.1:80 0.0.0.0:* users:((\"nginx\",pid=2,fd=5))\n" +
		"LISTEN 0 128 *:2222 *:* users:((\"sshd\",pid=9,fd=4))\n"
	got := ParseSS(out)
	if len(got) != 2 || got[0] != "127.0.0.1:22" || got[1] != "127.0.0.1:2222" {
		t.Fatalf("%v", got)
	}
}

func TestParseNetstat(t *testing.T) {
	out := "" +
		"  Proto  Local Address          Foreign Address        State           PID\n" +
		"  TCP    127.0.0.1:22           0.0.0.0:0              LISTENING       40\n" +
		"  TCP    0.0.0.0:80             0.0.0.0:0              LISTENING       12\n" +
		"  TCP    192.0.2.5:2222         0.0.0.0:0              LISTENING       40\n"
	got := ParseNetstat(out, map[string]bool{"40": true})
	if len(got) != 2 || got[0] != "127.0.0.1:22" || got[1] != "192.0.2.5:2222" {
		t.Fatalf("%v", got)
	}
}

func TestParseLsof(t *testing.T) {
	out := "sshd 9 root 3u IPv4 0x1 0t0 TCP 127.0.0.1:22 (LISTEN)\n" +
		"nginx 2 root 5u IPv4 0x2 0t0 TCP *:80 (LISTEN)\n"
	got := ParseLsof(out)
	if len(got) != 1 || got[0] != "127.0.0.1:22" {
		t.Fatalf("%v", got)
	}
}
