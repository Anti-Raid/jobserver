package rpc_messages

import (
	_ "github.com/Anti-Raid/jobserver/state" // Avoid unsafe import
)

// Spawns a job and executes it if the execute argument is set.
type Spawn struct {
	Name    string                 `json:"name"`
	Data    map[string]interface{} `json:"data"`
	Create  bool                   `json:"create"`
	Execute bool                   `json:"execute"`

	// If create is false, then task id must be set
	ID string `json:"id"`

	// The Guild ID which initiated the action
	GuildID string `json:"guild_id"`
}

type SpawnResponse struct {
	ID string `json:"id"`
}
