package windir

import "testing"

func TestProgramFilesPrefersW6432ThenProgramFiles(t *testing.T) {
	t.Setenv("ProgramW6432", `D:\Apps64`)
	t.Setenv("ProgramFiles", `D:\Apps32`)
	if got := ProgramFiles(); got != `D:\Apps64` {
		t.Fatalf("ProgramFiles = %q", got)
	}
	t.Setenv("ProgramW6432", "")
	if got := ProgramFiles(); got != `D:\Apps32` {
		t.Fatalf("ProgramFiles fallback = %q", got)
	}
	t.Setenv("ProgramFiles", "")
	if got := ProgramFiles(); got == "" {
		t.Fatal("empty ProgramFiles")
	}
}

func TestProgramDataPrefersEnv(t *testing.T) {
	t.Setenv("ProgramData", `D:\Data`)
	if got := ProgramData(); got != `D:\Data` {
		t.Fatalf("ProgramData = %q", got)
	}
}
