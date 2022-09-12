package main

import "maunium.net/go/maulogger/v2"

type SlackgoLogger struct {
	maulogger.Logger
}

// This makes slack-go able to use the logger to log debug output
func (l SlackgoLogger) Output(i int, s string) error {
	l.Debugln(s)
	return nil
}
