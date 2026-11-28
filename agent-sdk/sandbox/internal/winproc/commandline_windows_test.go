//go:build windows

package winproc

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/internal/winps"
)

func TestJobCommandLineChecksQuotedUTF16IncludingNUL(t *testing.T) {
	const application = `C:\Program Files\PowerShell\powershell.exe`
	empty, err := jobCommandLine(application, []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	boundary := strings.Repeat("x", 32767-len(empty)+1)
	line, err := jobCommandLine(application, []string{boundary})
	if err != nil || len(line) != 32767 || line[len(line)-1] != 0 {
		t.Fatalf("boundary = %d UTF-16 units, %v", len(line), err)
	}
	for _, args := range [][]string{
		{boundary + "x"}, {boundary[:len(boundary)-1] + "😀"}, {boundary + " "},
		winps.Args(strings.Repeat("x", 20000), winps.Options{}),
	} {
		if _, err := jobCommandLine(application, args); errorcode.CodeOf(err) != errorcode.InvalidArgument {
			t.Fatalf("oversize command = %v", err)
		}
	}
}
