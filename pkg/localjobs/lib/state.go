package lib

import (
	"context"
	"net/http"
	"runtime/debug"

	state "github.com/Anti-Raid/jobserver/pkg/server/state"
	jobstate "github.com/Anti-Raid/jobserver/state"
	"github.com/bwmarrin/discordgo"
)

// Implementor of jobstate.State
type State struct {
	GuildId       string
	DiscordSess   *discordgo.Session
	BotUser       *discordgo.User
	DebugInfoData *debug.BuildInfo
	ContextUse    context.Context
}

func (ts State) Transport() *http.Transport {
	transport := &http.Transport{}
	transport.RegisterProtocol("file", http.NewFileTransport(http.Dir("/")))
	transport.RegisterProtocol("job", state.NewRoundtripJobDl(ts.GuildId, transport))
	return transport
}

func (State) OperationMode() string {
	return "localjobs"
}

func (ts State) Discord() (*discordgo.Session, *discordgo.User, bool) {
	return ts.DiscordSess, ts.BotUser, false
}

func (ts State) DebugInfo() *debug.BuildInfo {
	return ts.DebugInfoData
}

func (ts State) Context() context.Context {
	return ts.ContextUse
}

type Progress struct{}

func (ts Progress) GetProgress() (*jobstate.Progress, error) {
	return &jobstate.Progress{
		State: "",
		Data:  map[string]any{},
	}, nil
}

func (ts Progress) SetProgress(prog *jobstate.Progress) error {
	return nil
}
