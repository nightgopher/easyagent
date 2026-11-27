package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigPathArgument(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
		fail bool
	}{
		{nil, "", false},
		{[]string{"serve", "--config", "/tmp/expert.yaml"}, "/tmp/expert.yaml", false},
		{[]string{"--config=expert.yaml", "serve"}, "expert.yaml", false},
		{[]string{"serve", "-config", "expert.yaml"}, "expert.yaml", false},
		{[]string{"-config=one", "--config=two"}, "two", false},
		{[]string{"--", "--config=ignored"}, "", false},
		{[]string{"--config"}, "", true},
		{[]string{"--config="}, "", true},
	} {
		got, err := configPathArgument(tc.args)
		if tc.fail {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		}
	}
}
