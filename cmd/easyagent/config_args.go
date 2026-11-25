package main

import (
	"fmt"
	"strings"
)

// configPathArgument selects the configuration before flags whose defaults
// depend on that file are registered. The normal flag parser still validates
// the entire command line afterwards.
func configPathArgument(args []string) (string, error) {
	var selected string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "--config" || arg == "-config" {
			if i+1 >= len(args) || args[i+1] == "" {
				return "", fmt.Errorf("%s requires a configuration file", arg)
			}
			i++
			selected = args[i]
			continue
		}
		for _, prefix := range []string{"--config=", "-config="} {
			if strings.HasPrefix(arg, prefix) {
				selected = strings.TrimPrefix(arg, prefix)
				if selected == "" {
					return "", fmt.Errorf("%s requires a configuration file", prefix)
				}
			}
		}
	}
	return selected, nil
}
