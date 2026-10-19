package controlcmd

import (
	"bytes"
	"context"
	"testing"
)

func TestInvalidControlCommandsDoNotRequireApplicationConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"help"}, 0}, {nil, 2}, {[]string{"unknown", "list"}, 2}, {[]string{"credentials", "bootstrap", "name"}, 2}, {[]string{"access", "grant"}, 2},
	} {
		var out, errOut bytes.Buffer
		if got := Run(context.Background(), tc.args, &out, &errOut); got != tc.code {
			t.Fatalf("args=%v code=%d stderr=%s", tc.args, got, &errOut)
		}
	}
}
