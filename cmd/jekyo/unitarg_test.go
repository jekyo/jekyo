package main

import "testing"

func TestUnitArg(t *testing.T) {
	unit := "ExecStart=/usr/local/bin/k3s \\\n    server \\\n\t'--advertise-address' \\\n\t'185.235.78.2' \\\n\t'--default-local-storage-path' \\\n\t'/storage' \\\n"
	if got := unitArg(unit, "--advertise-address"); got != "185.235.78.2" {
		t.Errorf("ip = %q", got)
	}
	if got := unitArg(unit, "--default-local-storage-path"); got != "/storage" {
		t.Errorf("storage = %q", got)
	}
}
