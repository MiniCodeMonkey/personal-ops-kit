package main

import (
	"strings"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/job"
)

// claudeCommand is the shell line the reader copies to pick an artifact up in an
// interactive Claude Code session: a `cd` first when the job names a project directory, and the artifact's path as
// the opening prompt so the session starts already reading it.
//
// A command to paste rather than a launch from the daemon, deliberately: the daemon
// runs with launchd's environment, and which terminal window
// should get the session is a decision only the person at the keyboard can make.
func claudeCommand(j *job.Job, artifactPath string) string {
	prompt := "Read " + artifactPath + " and help me act on its suggestions."
	cmd := "claude " + shellQuote(prompt)
	if j.ProjectDir != "" {
		cmd = "cd " + shellQuote(j.ProjectDir) + " && " + cmd
	}
	return cmd
}

// shellQuote single-quotes s for zsh; an embedded quote becomes '\” .
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
