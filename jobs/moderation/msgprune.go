package moderation

import (
	"bytes"
	"fmt"
	"time"

	"github.com/Anti-Raid/jobserver/common"
	"github.com/Anti-Raid/jobserver/interfaces"
	jobstate "github.com/Anti-Raid/jobserver/state"
	"github.com/Anti-Raid/jobserver/types"
	"github.com/Anti-Raid/jobserver/utils"
	"github.com/Anti-Raid/jobserver/utils/timex"
	"github.com/anti-raid/eureka/jsonimpl"
	"github.com/bwmarrin/discordgo"
	orderedmap "github.com/wk8/go-ordered-map/v2"
	"go.uber.org/zap"
)

var allowedMsgPruneChannelTypes = []discordgo.ChannelType{
	discordgo.ChannelTypeGuildText,
	discordgo.ChannelTypeGuildNews,
	discordgo.ChannelTypeGuildNewsThread,
	discordgo.ChannelTypeGuildPublicThread,
	discordgo.ChannelTypeGuildPrivateThread,
	discordgo.ChannelTypeGuildForum,
}

type MessagePrune struct {
	// Constraints, this is auto-set by the job in jobserver and hence not configurable in this mode.
	Constraints *ModerationConstraints

	// Backup options
	Options MessagePruneOpts
}

func (t *MessagePrune) Fields() map[string]any {
	return map[string]any{
		"Constraints": t.Constraints,
		"Options":     t.Options,
	}
}

func (t *MessagePrune) Expiry() *time.Duration {
	return nil
}

func (t *MessagePrune) Resumable() bool {
	return true
}

func (t *MessagePrune) Validate(state jobstate.State) error {
	opMode := state.OperationMode()
	if opMode == "jobs" {
		t.Constraints = FreePlanModerationConstraints // TODO: Add other constraint types based on plans once we have them
	} else if opMode == "localjobs" {
		if t.Constraints == nil {
			return fmt.Errorf("constraints are required")
		}
	} else {
		return fmt.Errorf("invalid operation mode")
	}

	if t.Options.PruneFrom > 14*24*timex.Hour || t.Options.PruneFrom == 0 {
		t.Options.PruneFrom = 14 * 24 * timex.Hour
	}

	if t.Options.MaxMessages == 0 {
		t.Options.MaxMessages = t.Constraints.MessagePrune.TotalMaxMessages
	}

	if t.Options.PerChannel < t.Constraints.MessagePrune.MinPerChannel {
		return fmt.Errorf("per_channel cannot be less than %d", t.Constraints.MessagePrune.MinPerChannel)
	}

	if t.Options.MaxMessages > t.Constraints.MessagePrune.TotalMaxMessages {
		return fmt.Errorf("max_messages cannot be greater than %d", t.Constraints.MessagePrune.TotalMaxMessages)
	}

	if t.Options.PerChannel > t.Options.MaxMessages {
		return fmt.Errorf("per_channel cannot be greater than max_messages")
	}

	if len(t.Options.SpecialAllocations) == 0 {
		t.Options.SpecialAllocations = make(map[string]int)
	}

	// Check current moderation concurrency
	count, _ := concurrentModerationState.LoadOrStore(state.GuildID(), 0)

	if count >= t.Constraints.MaxServerModeration {
		return fmt.Errorf("you already have more than %d moderation jobs in progress, please wait for it to finish", t.Constraints.MaxServerModeration)
	}

	return nil
}

func (t *MessagePrune) Exec(
	l *zap.Logger,
	state jobstate.State,
	progstate jobstate.ProgressState,
) (*types.Output, error) {
	discord, botUser, _ := state.Discord()
	ctx := state.Context()
	guildId := state.GuildID()

	// Check current moderation concurrency
	count, _ := concurrentModerationState.LoadOrStore(guildId, 0)

	if count >= t.Constraints.MaxServerModeration {
		return nil, fmt.Errorf("you already have more than %d moderation jobs in progress, please wait for it to finish", t.Constraints.MaxServerModeration)
	}

	concurrentModerationState.Store(guildId, count+1)

	// Decrement count when we're done
	defer func() {
		countNow, _ := concurrentModerationState.LoadOrStore(guildId, 0)

		if countNow > 0 {
			concurrentModerationState.Store(guildId, countNow-1)
		}
	}()

	l.Info("Fetching bots current state in server")
	m, err := discord.GuildMember(guildId, botUser.ID, discordgo.WithContext(ctx))

	if err != nil {
		return nil, fmt.Errorf("error fetching bots member object: %w", err)
	}

	// Fetch guild
	g, err := discord.Guild(guildId, discordgo.WithContext(ctx))

	if err != nil {
		return nil, fmt.Errorf("error fetching guild: %w", err)
	}

	// Fetch roles first before calculating base permissions
	if len(g.Roles) == 0 {
		roles, err := discord.GuildRoles(guildId, discordgo.WithContext(ctx))

		if err != nil {
			return nil, fmt.Errorf("error fetching roles: %w", err)
		}

		g.Roles = roles
	}

	if len(g.Channels) == 0 {
		channels, err := discord.GuildChannels(guildId, discordgo.WithContext(ctx))

		if err != nil {
			return nil, fmt.Errorf("error fetching channels: %w", err)
		}

		g.Channels = channels
	}

	// With servers now fully populated, get the base permissions now
	basePerms := utils.BasePermissions(g, m)

	if basePerms&discordgo.PermissionManageMessages != discordgo.PermissionManageMessages && basePerms&discordgo.PermissionAdministrator != discordgo.PermissionAdministrator {
		return nil, fmt.Errorf("bot does not have 'Manage Messages' permissions")
	}

	perChannelBackupMap, err := common.CreateChannelAllocations(
		basePerms,
		g,
		m,
		[]int64{discordgo.PermissionViewChannel, discordgo.PermissionReadMessageHistory, discordgo.PermissionManageMessages},
		allowedMsgPruneChannelTypes,
		common.GetChannelsFromList(g, t.Options.Channels),
		t.Options.SpecialAllocations,
		t.Options.PerChannel,
		t.Options.MaxMessages,
	)

	if err != nil {
		return nil, fmt.Errorf("error creating channel allocations: %w", err)
	}

	l.Info("Created channel allocations", zap.Any("alloc", perChannelBackupMap), zap.Strings("botDisplayIgnore", []string{"alloc"}))

	// Now handle all the channel allocations
	var finalMessagesEnd = orderedmap.New[string, []*discordgo.Message]()
	err = common.ChannelAllocationStream(
		perChannelBackupMap,
		func(channelID string, allocation int) (collected int, err error) {
			// Fetch messages and bulk delete
			currentId := ""
			finalMsgs := make([]*discordgo.Message, 0, allocation)
			for {
				// Fetch messages
				if allocation < len(finalMsgs) {
					// We've gone over, break
					break
				}

				limit := min(100, allocation-len(finalMsgs))

				l.Info("Fetching messages", zap.String("channelID", channelID), zap.Int("limit", limit), zap.String("currentId", currentId))

				// Fetch messages
				messages, err := discord.ChannelMessages(
					channelID,
					limit,
					currentId,
					"",
					"",
					discordgo.WithContext(ctx),
				)

				if err != nil {
					return len(finalMsgs), fmt.Errorf("error fetching messages: %w", err)
				}

				if len(messages) == 0 {
					break
				}

				var messageList = make([]string, 0, len(messages))

				var beyondPast = time.Now().Add(-1 * time.Duration(t.Options.PruneFrom))
				for _, m := range messages {
					// Check that the message is under beyondPast
					if m.Timestamp.Before(beyondPast) {
						continue
					}

					if t.Options.UserID != "" && m.Author.ID != t.Options.UserID {
						continue
					}

					messageList = append(messageList, m.ID)
					finalMsgs = append(finalMsgs, m)
				}

				if len(messageList) == 0 {
					break
				}

				// Bulk delete
				err = discord.ChannelMessagesBulkDelete(channelID, messageList, discordgo.WithContext(ctx))

				if err != nil {
					return len(finalMsgs), fmt.Errorf("error bulk deleting messages: %w", err)
				}

				if len(messages) < allocation {
					// We've reached the end
					break
				}

				currentId = messages[len(messages)-1].ID
			}

			finalMessagesEnd.Set(channelID, finalMsgs)

			return 0, nil
		},
		t.Options.MaxMessages,
		func() int {
			if t.Options.RolloverLeftovers {
				return t.Options.PerChannel
			}

			return 0
		}(),
	)

	if err != nil {
		return nil, fmt.Errorf("error handling channel allocations: %w", err)
	}

	var outputBuf bytes.Buffer

	// Write to buffer
	err = jsonimpl.MarshalToWriter(&outputBuf, finalMessagesEnd)

	if err != nil {
		return nil, fmt.Errorf("error encoding final messages: %w", err)
	}

	return &types.Output{
		Filename: "pruned-messages.txt",
		Buffer:   &outputBuf,
	}, nil
}

func (t *MessagePrune) Name() string {
	return "message_prune"
}

func (t *MessagePrune) LocalPresets() *interfaces.PresetInfo {
	return &interfaces.PresetInfo{
		Runnable: true,
		Preset: &MessagePrune{
			Constraints: &ModerationConstraints{
				MessagePrune: &MessagePruneConstraints{
					TotalMaxMessages: 1000,
					MinPerChannel:    10,
				},
				MaxServerModeration: 1,
			},
			Options: MessagePruneOpts{
				PerChannel: 100,
			},
		},
		Comments: map[string]string{
			"Constraints.MaxServerModeration":           "Only 1 mod job should be running at any given time locally",
			"Constraints.MessagePrune.TotalMaxMessages": "We can be more generous here with 1000 by default",
			"Constraints.MessagePrune.MinPerChannel":    "We can be more generous here with 10 by default",
		},
	}
}
