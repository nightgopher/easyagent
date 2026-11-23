package main

import "strings"

// navigateComposerHistory recalls submitted messages in the current session.
// A history browse starts only from an empty draft, leaving ordinary multiline
// editing and its arrow keys to the text editor.
func (s *session) navigateComposerHistory(up bool, current string) (string, bool) {
	if s.composerHistoryActive && current != s.composerHistoryValue {
		s.clearComposerHistory()
	}
	if !s.composerHistoryActive {
		if !up || current != "" {
			return "", false
		}
		for _, m := range s.Messages {
			if m != nil && m.User && strings.TrimSpace(m.Text) != "" {
				s.composerHistory = append(s.composerHistory, m.Text)
			}
		}
		if len(s.composerHistory) == 0 {
			return "", false
		}
		s.composerHistoryActive = true
		s.composerHistoryIndex = len(s.composerHistory)
	}
	if up {
		if s.composerHistoryIndex > 0 {
			s.composerHistoryIndex--
		}
	} else if s.composerHistoryIndex < len(s.composerHistory) {
		s.composerHistoryIndex++
	}
	value := ""
	if s.composerHistoryIndex < len(s.composerHistory) {
		value = s.composerHistory[s.composerHistoryIndex]
	}
	s.composerHistoryValue = value
	if !up && s.composerHistoryIndex == len(s.composerHistory) {
		s.clearComposerHistory()
	}
	return value, true
}

func (s *session) clearComposerHistory() {
	s.composerHistory = nil
	s.composerHistoryIndex = 0
	s.composerHistoryActive = false
	s.composerHistoryValue = ""
}
