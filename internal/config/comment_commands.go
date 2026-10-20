package config

// CommentCommandsConfig is tracker.comment_commands (#84): `/itervox`
// commands in GitHub issue comments.
type CommentCommandsConfig struct {
	// Enabled turns command polling on. Default false: a public repository
	// should opt in deliberately.
	Enabled bool
	// Allow lists GitHub logins that may run commands in addition to users
	// with write access (admin, maintain, write) to the repository.
	Allow []string
	// AllowTokenUser accepts commands from the account Itervox's own token
	// belongs to. Off by default: agents act through that account too, so
	// an agent (or a prompt-injected issue) could otherwise post a command.
	AllowTokenUser bool
	// ReplyToUnauthorized posts a short reply when someone without access
	// writes a command. Default false: such comments are ignored silently.
	ReplyToUnauthorized bool
}
