package backups

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Anti-Raid/jobserver/common"
	"github.com/Anti-Raid/jobserver/interfaces"
	jobstate "github.com/Anti-Raid/jobserver/state"
	"github.com/Anti-Raid/jobserver/step"
	"github.com/Anti-Raid/jobserver/types"
	"github.com/Anti-Raid/jobserver/utils"
	"github.com/go-viper/mapstructure/v2"

	iblfile "github.com/anti-raid/iblfile/go"
	"github.com/anti-raid/iblfile/go/encryptors/aes256"
	"github.com/anti-raid/iblfile/go/encryptors/noencryption"
	"github.com/bwmarrin/discordgo"
	"github.com/vmihailenco/msgpack/v5"
	"go.uber.org/zap"
)

func getImageAsDataUri(state jobstate.State, constraints *BackupConstraints, l *zap.Logger, f *iblfile.AutoEncryptedFile_FullFile, name, endpoint string, bo *BackupCreateOpts) (string, error) {
	if slices.Contains(bo.BackupGuildAssets, name) {
		l.Info("Fetching guild asset", zap.String("name", name))
		iconBytes, err := f.Get("assets/" + name)

		if err != nil {
			return "", fmt.Errorf("failed to get guild asset: %w", err)
		}

		return convertToDataUri("image/jpeg", iconBytes.Bytes()), nil
	} else {
		// Try fetching still, it might work
		client := http.Client{
			Timeout:   time.Duration(constraints.Restore.HttpClientTimeout),
			Transport: state.Transport(),
		}

		req, err := http.NewRequestWithContext(state.Context(), "GET", endpoint, nil)

		if err != nil {
			return "", fmt.Errorf("failed to create request: %w", err)
		}

		resp, err := client.Do(req)

		if err != nil {
			return "", fmt.Errorf("error fetching guild asset: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("error fetching guild asset: %w", fmt.Errorf("status code %d", resp.StatusCode))
		}

		mime := resp.Header.Get("Content-Type")

		if mime == "" {
			return "", fmt.Errorf("error fetching guild asset: %w", fmt.Errorf("no mime type"))
		}

		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)

		if err != nil {
			return "", fmt.Errorf("error reading guild icon: %w", err)
		}

		return convertToDataUri(mime, body), nil
	}
}

func readMsgpackSection[T any](f *iblfile.AutoEncryptedFile_FullFile, name string) (*T, error) {
	section, err := f.Get(name)

	if err != nil {
		return nil, fmt.Errorf("failed to get section %s: %w", name, err)
	}

	dec := msgpack.NewDecoder(section)
	dec.UseInternedStrings(true)
	dec.SetCustomStructTag("json")

	var outp T

	err = dec.Decode(&outp)

	if err != nil {
		return nil, fmt.Errorf("failed to decode section %s: %w", name, err)
	}

	return &outp, nil
}

func convertToDataUri(mimeType string, data []byte) string {
	// Base64 encode
	b64enc := base64.StdEncoding.EncodeToString(data)

	return fmt.Sprintf("data:%s;base64,%s", mimeType, b64enc)
}

func isRoleLessThanRole(a, b *discordgo.Role) bool {
	if a.Position == b.Position {
		return a.ID > b.ID // Newer roles are less than older roles
	}

	return a.Position < b.Position
}

// A job to restore a backup of a server
type ServerBackupRestore struct {
	// Constraints, this is auto-set by the job in jobserver and hence not configurable in this mode.
	Constraints *BackupConstraints

	// Backup options
	Options BackupRestoreOpts
}

func (t *ServerBackupRestore) Fields() map[string]any {
	opts := t.Options
	opts.Decrypt = "" // Clear encryption key

	return map[string]any{
		"Constraints": t.Constraints,
		"Options":     opts,
	}
}

func (t *ServerBackupRestore) Expiry() *time.Duration {
	return nil
}

func (t *ServerBackupRestore) Resumable() bool {
	return true
}

// Validate validates the job and sets up state if needed
func (t *ServerBackupRestore) Validate(state jobstate.State) error {
	opMode := state.OperationMode()
	if t.Constraints == nil || opMode == "jobs" {
		t.Constraints = FreePlanBackupConstraints // TODO: Add other constraint types based on plans once we have them
	}

	if t.Options.BackupSource == "" {
		return fmt.Errorf("backup_source is required")
	}

	if opMode == "jobs" {
		if !strings.HasPrefix(t.Options.BackupSource, "https://") && !strings.HasPrefix(t.Options.BackupSource, "job://") {
			return fmt.Errorf("backup_source must be a valid URL or a Job ID")
		}
	} else if opMode == "localjobs" {
		if !strings.HasPrefix(t.Options.BackupSource, "file://") && !strings.HasPrefix(t.Options.BackupSource, "http://") && !strings.HasPrefix(t.Options.BackupSource, "https://") {
			return fmt.Errorf("backup_source must be a valid URL or file path")
		}
	} else {
		return fmt.Errorf("invalid operation mode")
	}

	switch t.Options.ChannelRestoreMode {
	case ChannelRestoreModeFull:
	case ChannelRestoreModeDiff:
		return fmt.Errorf("channel_restore_mode 'diff' is not yet supported due to the complexity of the approach")
	case ChannelRestoreModeIgnoreExisting:
	default:
		if string(t.Options.ChannelRestoreMode) == "" {
			t.Options.ChannelRestoreMode = ChannelRestoreModeFull
		} else {
			return fmt.Errorf("invalid channel_restore_mode")
		}
	}

	// Check current backup concurrency
	count, _ := concurrentBackupState.LoadOrStore(state.GuildID(), 0)

	if count >= t.Constraints.MaxServerBackups {
		return fmt.Errorf("you already have more than %d backup-related jobs in progress, please wait for it to finish", t.Constraints.MaxServerBackups)
	}

	return nil
}

func (t *ServerBackupRestore) Exec(
	l *zap.Logger,
	state jobstate.State,
	progstate jobstate.ProgressState,
) (*types.Output, error) {
	discord, botUser, _ := state.Discord()
	ctx := state.Context()
	guildId := state.GuildID()

	// Check current backup concurrency
	count, _ := concurrentBackupState.LoadOrStore(guildId, 0)

	if count >= t.Constraints.MaxServerBackups {
		return nil, fmt.Errorf("you already have more than %d backup-related jobs in progress, please wait for it to finish", t.Constraints.MaxServerBackups)
	}

	concurrentBackupState.Store(guildId, count+1)

	// Decrement count when we're done
	defer func() {
		countNow, _ := concurrentBackupState.LoadOrStore(guildId, 0)

		if countNow > 0 {
			concurrentBackupState.Store(guildId, countNow-1)
		}
	}()

	// Download backup
	l.Info("Downloading backup", zap.String("url", t.Options.BackupSource))
	client := http.Client{
		Timeout:   time.Duration(t.Constraints.Restore.HttpClientTimeout),
		Transport: state.Transport(),
	}

	req, err := http.NewRequestWithContext(ctx, "GET", t.Options.BackupSource, nil)

	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)

	if err != nil {
		return nil, fmt.Errorf("failed to download backup: %w", err)
	}

	l.Info("Backup source responded", zap.Int("status_code", resp.StatusCode), zap.Int64("contentLength", resp.ContentLength))

	// Limit body size to MaxBodySize
	if resp.ContentLength > t.Constraints.Restore.MaxBodySize {
		return nil, fmt.Errorf("backup too large, expected less than %d bytes, got %d bytes", t.Constraints.Restore.MaxBodySize, resp.ContentLength)
	}

	resp.Body = http.MaxBytesReader(nil, resp.Body, t.Constraints.Restore.MaxBodySize)

	defer resp.Body.Close()

	l.Info("Parsing backup", zap.String("url", t.Options.BackupSource))

	// Parse backup
	t1 := time.Now()

	var aeSource iblfile.AutoEncryptor

	if t.Options.Decrypt == "" {
		aeSource = noencryption.NoEncryptionSource{}
	} else {
		aeSource = aes256.AES256Source{
			EncryptionKey: t.Options.Decrypt,
		}
	}

	t.Options.Decrypt = "" // Clear encryption key

	f, err := iblfile.OpenAutoEncryptedFile_FullFile(resp.Body, aeSource)

	if err != nil {
		return nil, fmt.Errorf("error loading file: %w, is the password correct", err)
	}

	t2 := time.Now()

	l.Debug("STATISTICS: openautoencryptedfile", zap.Float64("duration", t2.Sub(t1).Seconds()))

	t1 = time.Now()

	sections, err := f.Sections()

	if err != nil {
		return nil, fmt.Errorf("error getting sections: %w", err)
	}

	keys := make([]string, 0, len(sections))
	for name := range sections {
		keys = append(keys, name)
	}

	t2 = time.Now()

	l.Debug("STATISTICS: keys", zap.Float64("duration", t2.Sub(t1).Seconds()), zap.Strings("keys", keys))

	// Step 0. Fetch backup_opts
	t1 = time.Now()

	bo, err := readMsgpackSection[BackupCreateOpts](f, "backup_opts")

	if err != nil {
		return nil, fmt.Errorf("failed to get backup_opts: %w", err)
	}

	t2 = time.Now()

	l.Debug("STATISTICS: backupopts", zap.Float64("duration", t2.Sub(t1).Seconds()))

	// Fetch the bots member object in the guild
	l.Info("Fetching bots current state in server")
	m, err := discord.GuildMember(guildId, botUser.ID, discordgo.WithContext(ctx))

	if err != nil {
		return nil, fmt.Errorf("error fetching bots member object: %w", err)
	}

	l.Info("Fetching guild object")
	tgtGuild, err := discord.Guild(guildId, discordgo.WithContext(ctx))

	if err != nil {
		return nil, fmt.Errorf("error fetching guild: %w", err)
	}

	// Fetch roles first before calculating base permissions
	if len(tgtGuild.Roles) == 0 {
		roles, err := discord.GuildRoles(guildId, discordgo.WithContext(ctx))

		if err != nil {
			return nil, fmt.Errorf("error fetching roles: %w", err)
		}

		tgtGuild.Roles = roles
	}

	basePerms := utils.BasePermissions(tgtGuild, m)

	if !utils.CheckPermission(basePerms, discordgo.PermissionManageChannels) {
		return nil, fmt.Errorf("bot does not have 'Manage Channels' permissions")
	}

	if !utils.CheckPermission(basePerms, discordgo.PermissionManageRoles) {
		return nil, fmt.Errorf("bot does not have 'Manage Roles' permissions")
	}

	if !utils.CheckPermission(basePerms, discordgo.PermissionManageWebhooks) {
		return nil, fmt.Errorf("bot does not have 'Manage Webhooks' permissions")
	}

	// Get highest role
	var tgtBotGuildHighestRole *discordgo.Role

	for _, role := range tgtGuild.Roles {
		if !slices.Contains(m.Roles, role.ID) {
			continue
		}

		if tgtBotGuildHighestRole == nil {
			tgtBotGuildHighestRole = role
			continue
		}

		if role.Position > tgtBotGuildHighestRole.Position {
			tgtBotGuildHighestRole = role
		}

		if role.Position == tgtBotGuildHighestRole.Position {
			// Check ID
			if role.ID > tgtBotGuildHighestRole.ID {
				tgtBotGuildHighestRole = role
			}
		}
	}

	if tgtBotGuildHighestRole == nil {
		return nil, fmt.Errorf("bot does not have any roles")
	}

	if tgtBotGuildHighestRole.Position <= 0 {
		return nil, fmt.Errorf("bot role isnt high enough")
	}

	// Fetch channels of guild
	channels, err := discord.GuildChannels(guildId, discordgo.WithContext(ctx))

	if err != nil {
		return nil, fmt.Errorf("error fetching channels: %w", err)
	}

	tgtGuild.Channels = channels

	l.Info("Got bots highest role", zap.String("role_id", tgtBotGuildHighestRole.ID), zap.Int("role_position", tgtBotGuildHighestRole.Position))

	// Step 1. Fetch guild data
	srcGuild, err := readMsgpackSection[discordgo.Guild](f, "core/guild")

	if err != nil {
		return nil, fmt.Errorf("failed to get core data: %w", err)
	}

	if srcGuild.ID == "" {
		return nil, fmt.Errorf("guild data is invalid [id is empty], likely an internal decoding error")
	}

	var srcIsCommunity = slices.Contains(srcGuild.Features, discordgo.GuildFeatureCommunity)
	var tgtIsCommunity = slices.Contains(tgtGuild.Features, discordgo.GuildFeatureCommunity)

	if srcIsCommunity && !tgtIsCommunity {
		return nil, fmt.Errorf("cannot restore community server to non-community server")
	}

	// Resumability starts here
	outp, err := step.NewStepper(
		step.Step[ServerBackupRestore]{
			State: "edit_base_guild",
			Exec: func(t *ServerBackupRestore, l *zap.Logger, state jobstate.State, progstate jobstate.ProgressState, progress *jobstate.Progress) (*types.Output, *jobstate.Progress, error) {
				// Edit basic guild. Note that settings related to ID's are changed later if needed
				// Notes:
				//
				// - Region is not restored
				// - Owner is not restored
				gp := &discordgo.GuildParams{
					Name:                        srcGuild.Name,
					Description:                 srcGuild.Description,
					DefaultMessageNotifications: int(srcGuild.DefaultMessageNotifications),
					AfkTimeout:                  srcGuild.AfkTimeout,
				}

				// If the src server is a community server or the target isn't, we can restore these settings
				if srcIsCommunity || !tgtIsCommunity {
					gp.ExplicitContentFilter = int(srcGuild.ExplicitContentFilter)
					gp.VerificationLevel = &srcGuild.VerificationLevel
				}

				// Icons
				canUseIcon := slices.Contains(srcGuild.Features, "ANIMATED_ICON") || !strings.HasPrefix(srcGuild.Icon, "a_")
				canUseBanner := (slices.Contains(srcGuild.Features, "BANNER") && !strings.HasPrefix(srcGuild.Banner, "a_")) || slices.Contains(srcGuild.Features, "ANIMATED_BANNER")
				canUseSplash := slices.Contains(srcGuild.Features, "INVITE_SPLASH") && !strings.HasPrefix(srcGuild.Splash, "a_")

				if srcGuild.Icon != "" {
					if !canUseIcon {
						l.Warn("Not restoring animated icon on unsupported server", zap.String("guild_id", srcGuild.ID))
					} else {
						icon, err := getImageAsDataUri(state, t.Constraints, l, f, "guildIcon", srcGuild.IconURL("1024"), bo)

						if err != nil {
							return nil, nil, fmt.Errorf("failed to get icon: %w", err)
						}

						gp.Icon = icon
					}
				}

				if srcGuild.Banner != "" {
					if !canUseBanner {
						l.Warn("Not restoring banner on unsupported server", zap.String("guild_id", srcGuild.ID))
					} else {
						banner, err := getImageAsDataUri(state, t.Constraints, l, f, "guildBanner", srcGuild.BannerURL("1024"), bo)

						if err != nil {
							return nil, nil, fmt.Errorf("failed to get banner: %w", err)
						}

						gp.Banner = banner
					}
				}

				if srcGuild.Splash != "" {
					if !canUseSplash {
						l.Warn("Not restoring splash on unsupported server", zap.String("guild_id", srcGuild.ID))
					} else {
						splash, err := getImageAsDataUri(state, t.Constraints, l, f, "guildSplash", discordgo.EndpointGuildSplash(srcGuild.ID, srcGuild.Splash), bo)

						if err != nil {
							return nil, nil, fmt.Errorf("failed to get splash: %w", err)
						}

						gp.Splash = splash
					}
				}

				_, err = discord.GuildEdit(guildId, gp, discordgo.WithContext(ctx))

				if err != nil {
					return nil, nil, fmt.Errorf("failed to edit guild: %w", err)
				}

				return nil, &jobstate.Progress{}, nil
			},
		},
		step.Step[ServerBackupRestore]{
			State: "delete_old_roles",
			Exec: func(t *ServerBackupRestore, l *zap.Logger, state jobstate.State, progstate jobstate.ProgressState, progress *jobstate.Progress) (*types.Output, *jobstate.Progress, error) {
				for _, r := range tgtGuild.Roles {
					if slices.Contains(t.Options.ProtectedRoles, r.ID) {
						continue
					}

					if r.Managed {
						continue
					}

					if r.ID == tgtGuild.ID {
						continue // @everyone
					}

					if r.ID == tgtBotGuildHighestRole.ID {
						continue // Bot role
					}

					if r.Position > tgtBotGuildHighestRole.Position {
						continue // Higher than bot role
					}

					if r.Position == tgtBotGuildHighestRole.Position && tgtBotGuildHighestRole.ID > r.ID {
						continue
					}

					l.Info("Deleting role", zap.String("name", r.Name), zap.Int("position", r.Position), zap.String("id", r.ID))

					err := discord.GuildRoleDelete(guildId, r.ID, discordgo.WithRetryOnRatelimit(true), discordgo.WithContext(ctx))

					if err != nil && !t.Options.IgnoreRestoreErrors {
						return nil, nil, fmt.Errorf("failed to delete role: %w with position of %d", err, r.Position)
					} else if t.Options.IgnoreRestoreErrors && err != nil {
						l.Warn("Failed to delete role but ignoring error", zap.String("name", r.Name), zap.Int("position", r.Position), zap.String("id", r.ID), zap.Error(err))
					}

					time.Sleep(time.Duration(t.Constraints.Restore.RoleDeleteSleep))
				}

				return nil, &jobstate.Progress{}, nil
			},
		},
		step.Step[ServerBackupRestore]{
			State: "create_new_roles",
			Exec: func(t *ServerBackupRestore, l *zap.Logger, state jobstate.State, progstate jobstate.ProgressState, progress *jobstate.Progress) (*types.Output, *jobstate.Progress, error) {
				var prevState struct {
					RestoredRoleMap map[string]string `mapstructure:"restoredRoleMap,omitempty"`
				}

				err := mapstructure.Decode(progress.Data, &prevState)

				if err != nil {
					return nil, nil, fmt.Errorf("failed to decode progress data: %w", err)
				}

				// Map of backed up role id to restored role id
				var restoredRolesMap map[string]string

				if len(prevState.RestoredRoleMap) > 0 {
					restoredRolesMap = prevState.RestoredRoleMap
				} else {
					restoredRolesMap = make(map[string]string)
				}

				// SortFunc sorts the slice x in ascending order as determined by the cmp function.
				//
				// This sort is not guaranteed to be stable. cmp(a, b) should return a negative number when a < b,
				// a positive number when a > b and zero when a == b
				// or a and b are incomparable in the sense of a strict weak ordering.
				// Sort in descending order
				slices.SortFunc(srcGuild.Roles, func(a, b *discordgo.Role) int {
					if isRoleLessThanRole(a, b) {
						return 1
					} else {
						return -1
					}
				})

				// First create the roles, we'll modify their permissions later (discord doesn't guarantee the order of role creation)
				for i := range srcGuild.Roles {
					// Already done
					if _, ok := restoredRolesMap[srcGuild.Roles[i].ID]; ok {
						continue
					}

					if slices.Contains(t.Options.ProtectedRoles, srcGuild.Roles[i].ID) {
						continue
					}

					if srcGuild.Roles[i].Managed {
						continue
					}

					if srcGuild.Roles[i].ID == srcGuild.ID {
						continue // @everyone
					}

					l.Info("Creating role", zap.String("name", srcGuild.Roles[i].Name), zap.Int("finalPosition", srcGuild.Roles[i].Position), zap.String("id", srcGuild.Roles[i].ID))

					newRole, err := discord.GuildRoleCreate(guildId, &discordgo.RoleParams{
						Name: srcGuild.Roles[i].Name,
						Color: func() *int {
							if srcGuild.Roles[i].Color == 0 {
								return nil
							}

							return &srcGuild.Roles[i].Color
						}(),
						Hoist:       &srcGuild.Roles[i].Hoist,
						Permissions: &srcGuild.Roles[i].Permissions,
						Mentionable: &srcGuild.Roles[i].Mentionable,
					}, discordgo.WithRetryOnRatelimit(true), discordgo.WithContext(ctx))

					if err != nil {
						return nil, nil, fmt.Errorf("failed to create role: %w", err)
					}

					restoredRolesMap[srcGuild.Roles[i].ID] = newRole.ID

					// Save intermediate result of making the new role to allow better resumability
					err = common.SaveIntermediateResult(progstate, progress, map[string]any{
						"restoredRoleMap": restoredRolesMap,
					})

					if err != nil {
						return nil, nil, fmt.Errorf("failed to save intermediate result: %w", err)
					}

					time.Sleep(time.Duration(t.Constraints.Restore.RoleCreateSleep))
				}

				return nil, &jobstate.Progress{
					Data: map[string]any{
						"restoredRoleMap": restoredRolesMap,
					},
				}, nil
			},
		},
		step.Step[ServerBackupRestore]{
			State: "delete_old_channels",
			Exec: func(t *ServerBackupRestore, l *zap.Logger, state jobstate.State, progstate jobstate.ProgressState, progress *jobstate.Progress) (*types.Output, *jobstate.Progress, error) {
				var srcChannelMap = make(map[string]*discordgo.Channel) // Map of backed up channel id to channel object
				for _, channel := range srcGuild.Channels {
					srcChannelMap[channel.ID] = channel
				}

				var ignoredChannels []string
				for i := range tgtGuild.Channels {
					if slices.Contains(t.Options.ProtectedChannels, tgtGuild.Channels[i].ID) {
						continue
					}

					if tgtGuild.Channels[i].ID == tgtGuild.RulesChannelID || tgtGuild.Channels[i].ID == tgtGuild.PublicUpdatesChannelID {
						continue
					}

					if tgtGuild.Channels[i].ID == tgtGuild.PublicUpdatesChannelID {
						continue
					}

					bp := utils.MemberChannelPerms(basePerms, tgtGuild, m, tgtGuild.Channels[i])

					if !utils.CheckPermission(bp, discordgo.PermissionManageChannels) {
						l.Warn("Not removing channel due to lack of 'Manage Channels' permissions", zap.String("channel_id", tgtGuild.Channels[i].ID))
						continue
					}

					switch t.Options.ChannelRestoreMode {
					case ChannelRestoreModeIgnoreExisting:
						if _, ok := srcChannelMap[tgtGuild.Channels[i].ID]; ok {
							ignoredChannels = append(ignoredChannels, tgtGuild.Channels[i].ID)
							continue
						}
					}

					l.Info("Deleting channel", zap.String("name", tgtGuild.Channels[i].Name), zap.Int("position", tgtGuild.Channels[i].Position), zap.String("id", tgtGuild.Channels[i].ID))

					_, err := discord.ChannelDelete(tgtGuild.Channels[i].ID, discordgo.WithRetryOnRatelimit(true), discordgo.WithContext(ctx))

					if err != nil {
						return nil, nil, fmt.Errorf("failed to delete channel: %w", err)
					}

					time.Sleep(time.Duration(t.Constraints.Restore.ChannelDeleteSleep))
				}

				return nil, &jobstate.Progress{
					Data: map[string]any{
						"ignoredChannels": ignoredChannels,
					},
				}, nil
			},
		},
		step.Step[ServerBackupRestore]{
			State: "create_new_channels",
			Exec: func(t *ServerBackupRestore, l *zap.Logger, state jobstate.State, progstate jobstate.ProgressState, progress *jobstate.Progress) (*types.Output, *jobstate.Progress, error) {
				var prevState struct {
					IgnoredChannels     []string          `mapstructure:"ignoredChannels"`
					RestoredRoleMap     map[string]string `mapstructure:"restoredRoleMap"`
					RestoredChannelsMap map[string]string `mapstructure:"restoredChannelsMap"`
				}

				err := mapstructure.Decode(progress.Data, &prevState)

				if err != nil {
					return nil, nil, fmt.Errorf("failed to decode progress data: %w", err)
				}

				ignoredChannels := prevState.IgnoredChannels
				restoredRolesMap := prevState.RestoredRoleMap

				// Map of backed up channel id to restored channel id
				var restoredChannelsMap map[string]string

				// Restore it from previous state if available
				if len(prevState.RestoredChannelsMap) > 0 {
					l.Info("Restoring channels from previous state")
					restoredChannelsMap = prevState.RestoredChannelsMap
				} else {
					restoredChannelsMap = make(map[string]string)
				}

				// Internal function. Given a channel, this fixes permission overwrites and then creates the channel from the old source channel
				var createChannel = func(channel *discordgo.Channel) (*discordgo.Channel, error) {
					l.Info("Creating channel", zap.String("name", channel.Name), zap.Int("position", channel.Position), zap.String("srcId", channel.ID), zap.String("parent_id", channel.ParentID), zap.Any("type", channel.Type))

					// fix permission overwrites
					var permOverwrites = []*discordgo.PermissionOverwrite{}

					for _, overwrite := range channel.PermissionOverwrites {
						if overwrite.Type == discordgo.PermissionOverwriteTypeRole {
							if rcid, ok := restoredRolesMap[overwrite.ID]; ok {
								permOverwrites = append(permOverwrites, &discordgo.PermissionOverwrite{
									ID:    rcid,
									Type:  overwrite.Type,
									Allow: overwrite.Allow,
									Deny:  overwrite.Deny,
								})
								continue
							}

							if overwrite.ID == srcGuild.ID {
								permOverwrites = append(permOverwrites, &discordgo.PermissionOverwrite{
									ID:    tgtGuild.ID,
									Type:  overwrite.Type,
									Allow: overwrite.Allow,
									Deny:  overwrite.Deny,
								})
							}
						} else {
							permOverwrites = append(permOverwrites, overwrite)
						}
					}

					c, err := discord.GuildChannelCreateComplex(guildId, discordgo.GuildChannelCreateData{
						Name:                 channel.Name,
						Type:                 channel.Type,
						Topic:                channel.Topic,
						Bitrate:              channel.Bitrate,
						UserLimit:            channel.UserLimit,
						RateLimitPerUser:     channel.RateLimitPerUser,
						Position:             channel.Position,
						PermissionOverwrites: permOverwrites,
						ParentID:             channel.ParentID,
						NSFW:                 channel.NSFW,
					}, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))

					if err != nil {
						return nil, fmt.Errorf("failed to create channel: %w", err)
					}

					return c, nil
				}

				// First restore categories
				for i := range srcGuild.Channels {
					if srcGuild.Channels[i].Type != discordgo.ChannelTypeGuildCategory {
						continue
					}

					// Already done
					if _, ok := restoredChannelsMap[srcGuild.Channels[i].ID]; ok {
						continue
					}

					// Ignore existing
					if slices.Contains(ignoredChannels, srcGuild.Channels[i].ID) {
						restoredChannelsMap[srcGuild.Channels[i].ID] = srcGuild.Channels[i].ID
						continue
					}

					nc, err := createChannel(srcGuild.Channels[i])

					if err != nil {
						return nil, nil, fmt.Errorf("failed to create channel: %w", err)
					}

					restoredChannelsMap[srcGuild.Channels[i].ID] = nc.ID

					// Save intermediare result of making the new role to allow better resumability
					err = common.SaveIntermediateResult(progstate, progress, map[string]any{
						"restoredChannelsMap": restoredChannelsMap,
					})

					if err != nil {
						return nil, nil, fmt.Errorf("failed to save intermediate result: %w", err)
					}

					time.Sleep(time.Duration(t.Constraints.Restore.ChannelCreateSleep))
				}

				// Next restore channels
				for i := range srcGuild.Channels {
					if _, ok := restoredChannelsMap[srcGuild.Channels[i].ID]; ok {
						continue
					}

					// Ignore existing
					if slices.Contains(ignoredChannels, srcGuild.Channels[i].ID) {
						restoredChannelsMap[srcGuild.Channels[i].ID] = srcGuild.Channels[i].ID
						continue
					}

					// Create corresponding category if needed
					if srcGuild.Channels[i].ParentID != "" {
						if rcid, ok := restoredChannelsMap[srcGuild.Channels[i].ParentID]; ok {
							srcGuild.Channels[i].ParentID = rcid
						} else {
							if t.Options.IgnoreRestoreErrors {
								l.Warn("Parent channel does not exist, skipping", zap.String("channel_id", srcGuild.Channels[i].ParentID))
								srcGuild.Channels[i].ParentID = ""
							} else {
								return nil, nil, fmt.Errorf("parent channel does not exist")
							}
						}
					}

					nc, err := createChannel(srcGuild.Channels[i])

					if err != nil {
						return nil, nil, fmt.Errorf("failed to create channel: %w", err)
					}

					restoredChannelsMap[srcGuild.Channels[i].ID] = nc.ID

					// Save intermediare result of making the new role to allow better resumability
					err = common.SaveIntermediateResult(progstate, progress, map[string]any{
						"restoredChannelsMap": restoredChannelsMap,
					})

					if err != nil {
						return nil, nil, fmt.Errorf("failed to save intermediate result: %w", err)
					}

					time.Sleep(time.Duration(t.Constraints.Restore.ChannelCreateSleep))
				}

				return nil, &jobstate.Progress{
					Data: map[string]any{
						"restoredChannelsMap": restoredChannelsMap,
					},
				}, nil
			},
		},
		step.Step[ServerBackupRestore]{
			State: "update_guild_features",
			Exec: func(t *ServerBackupRestore, l *zap.Logger, state jobstate.State, progstate jobstate.ProgressState, progress *jobstate.Progress) (*types.Output, *jobstate.Progress, error) {
				var prevState struct {
					RestoredChannelsMap map[string]string `mapstructure:"restoredChannelsMap"`
				}

				err := mapstructure.Decode(progress.Data, &prevState)

				if err != nil {
					return nil, nil, fmt.Errorf("failed to decode progress data: %w", err)
				}

				restoredChannelsMap := prevState.RestoredChannelsMap

				gp := &discordgo.GuildParams{}

				// Features, only COMMUNITY is editable IIRC
				var features []discordgo.GuildFeature = tgtGuild.Features
				if basePerms&discordgo.PermissionAdministrator == discordgo.PermissionAdministrator {
					if slices.Contains(srcGuild.Features, discordgo.GuildFeatureCommunity) && !slices.Contains(features, discordgo.GuildFeatureCommunity) {
						var rulesChannelId string
						var publicUpdatesChannelId string

						for srcChannel, restoredChannel := range restoredChannelsMap {
							if srcChannel == srcGuild.RulesChannelID {
								rulesChannelId = restoredChannel
							}

							if srcChannel == srcGuild.PublicUpdatesChannelID {
								publicUpdatesChannelId = restoredChannel
							}

							if rulesChannelId != "" && publicUpdatesChannelId != "" {
								break
							}
						}

						gp.RulesChannelID = rulesChannelId
						gp.PublicUpdatesChannelID = publicUpdatesChannelId

						if gp.RulesChannelID != "" && gp.PublicUpdatesChannelID != "" {
							features = append(features, discordgo.GuildFeatureCommunity)
							if tgtGuild.VerificationLevel == discordgo.VerificationLevelNone || tgtGuild.VerificationLevel == discordgo.VerificationLevelLow {
								medium := discordgo.VerificationLevelMedium
								gp.VerificationLevel = &medium
							}
						}
					}
				} else {
					l.Warn("Not admin, certain features cannot be editted (e.g. COMMUNITY)", zap.Int64("basePerms", basePerms))
				}

				gp.Features = features

				_, err = discord.GuildEdit(guildId, gp, discordgo.WithRetryOnRatelimit(true), discordgo.WithContext(ctx))

				if err != nil {
					return nil, nil, fmt.Errorf("failed to edit guild: %w", err)
				}

				return nil, &jobstate.Progress{}, nil
			},
		},
		step.Step[ServerBackupRestore]{
			State: "create_webhook_if_needed",
			Exec: func(t *ServerBackupRestore, l *zap.Logger, state jobstate.State, progstate jobstate.ProgressState, progress *jobstate.Progress) (*types.Output, *jobstate.Progress, error) {
				if bo.BackupMessages {
					l.Info("Waiting 5 seconds to avoid API issues")

					time.Sleep(5 * time.Second)

					var prevState struct {
						RestoredChannelsMap map[string]string `mapstructure:"restoredChannelsMap"`
					}

					err := mapstructure.Decode(progress.Data, &prevState)

					if err != nil {
						return nil, nil, fmt.Errorf("failed to decode progress data: %w", err)
					}

					// Get first restored channel
					if len(prevState.RestoredChannelsMap) == 0 {
						return nil, nil, nil // No channels restored, skip step
					}

					// Fetch tgtGuild again to get the latest channels
					tgtGuild, err = discord.Guild(guildId, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))

					if err != nil {
						return nil, nil, fmt.Errorf("failed to fetch guild: %w", err)
					}

					// Get first channel
					var channelId string

					for _, channel := range tgtGuild.Channels {
						if channel.Type == discordgo.ChannelTypeGuildText || channel.Type == discordgo.ChannelTypeGuildNews || channel.Type == discordgo.ChannelTypeGuildVoice {
							channelId = channel.ID
							break
						}
					}

					if channelId == "" {
						l.Warn("No channels to create webhook in")
						return nil, nil, nil
					}

					// Create webhook for sending messages to any channel
					webhook, err := discord.WebhookCreate(channelId, "Anti-Raid Message Restore", "", discordgo.WithContext(ctx))

					if err != nil {
						if t.Options.IgnoreRestoreErrors {
							l.Warn("Failed to create webhook. Skipping message send", zap.Error(err), zap.String("channel_id", channelId))
							return nil, nil, nil
						}

						l.Warn("Failed to create webhook. Skipping message send", zap.Error(err), zap.String("channel_id", channelId))

						return nil, nil, fmt.Errorf("failed to create message send webhook: %w", err)
					}

					return nil, &jobstate.Progress{
						Data: map[string]any{
							"webhook_id":    webhook.ID,
							"webhook_token": webhook.Token,
						},
					}, nil
				}

				return nil, nil, nil
			},
		},
		step.Step[ServerBackupRestore]{
			State: "restore_messages",
			Exec: func(t *ServerBackupRestore, l *zap.Logger, state jobstate.State, progstate jobstate.ProgressState, progress *jobstate.Progress) (*types.Output, *jobstate.Progress, error) {
				if bo.BackupMessages {
					l.Info("Waiting 5 seconds to avoid API issues")

					var prevState struct {
						RestoredChannelsMap map[string]string   `mapstructure:"restoredChannelsMap"`
						DoneChannels        map[string][]string `mapstructure:"doneChannels"`
						WebhookID           string              `mapstructure:"webhook_id"`
						WebhookToken        string              `mapstructure:"webhook_token"`
					}

					err := mapstructure.Decode(progress.Data, &prevState)

					if err != nil {
						return nil, nil, fmt.Errorf("failed to decode progress data: %w", err)
					}

					var totalMessages int
					if len(prevState.DoneChannels) == 0 {
						prevState.DoneChannels = make(map[string][]string)
					} else {
						for _, v := range prevState.DoneChannels {
							totalMessages += len(v)
						}
					}

					if prevState.WebhookID != "" {
						defer func() {
							// Unfortunately, discordgo's impl here is broken so we can't do error handling here
							discord.WebhookDeleteWithToken(prevState.WebhookID, prevState.WebhookToken, discordgo.WithContext(ctx))
						}()
					} else {
						return nil, nil, nil // No webhook, skip step
					}

					restoredChannelsMap := prevState.RestoredChannelsMap

					var currentChannelMap = make(map[string]*discordgo.Channel) // Map of current channel id to channel object
					for _, channel := range tgtGuild.Channels {
						currentChannelMap[channel.ID] = channel
					}

					for backedUpChannelId, restoredChannelId := range restoredChannelsMap {
						if _, ok := sections["messages/"+backedUpChannelId]; !ok {
							continue
						}

						if _, ok := prevState.DoneChannels[restoredChannelId]; ok {
							continue // Don't do done channels
						}

						l.Info("Processing backed up channel messages", zap.String("backed_up_channel_id", backedUpChannelId), zap.String("restored_channel_id", restoredChannelId))

						perms := utils.MemberChannelPerms(basePerms, tgtGuild, m, currentChannelMap[restoredChannelId])
						canManageWebhooks := perms&discordgo.PermissionManageWebhooks == discordgo.PermissionManageWebhooks

						if !canManageWebhooks {
							l.Error("Bot does not have 'Manage Webhooks' permissions in this channel, ignoring it...", zap.String("channel_id", restoredChannelId))
							continue
						}

						// Fetch section
						bmPtr, err := readMsgpackSection[[]*BackupMessage](f, "messages/"+backedUpChannelId)

						if err != nil {
							if t.Options.IgnoreRestoreErrors {
								continue
							}
							return nil, nil, fmt.Errorf("failed to get section: %w", err)
						}

						bm := *bmPtr

						// Modify the webhook to this channel
						_, err = discord.WebhookEdit(prevState.WebhookID, "Anti-Raid Message Restore", "", restoredChannelId, discordgo.WithContext(ctx))

						if err != nil {
							if t.Options.IgnoreRestoreErrors {
								l.Warn("Failed to edit webhook", zap.Error(err))
								continue
							}

							return nil, nil, fmt.Errorf("failed to edit webhook: %w", err)
						}

						// Now send the messages, reversing the order due to how Get Channel Messages works
						for i := len(bm) - 1; i >= 0; i-- {
							if totalMessages > t.Constraints.Create.TotalMaxMessages {
								l.Warn("Hit total max messages limit, stopping", zap.Int("totalMessages", totalMessages))
								break
							}

							// Check if the message is already sent
							if slices.Contains(prevState.DoneChannels[restoredChannelId], bm[i].Message.ID) {
								continue
							}

							var rm = discordgo.WebhookParams{
								Content:         bm[i].Message.Content,
								Username:        bm[i].Message.Author.Username,
								AvatarURL:       bm[i].Message.Author.AvatarURL(""),
								Embeds:          bm[i].Message.Embeds,
								TTS:             false, // Set later on
								Components:      bm[i].Message.Components,
								AllowedMentions: &discordgo.MessageAllowedMentions{},
							}

							if len(rm.Content) > 2000 {
								// Upload as file
								content := rm.Content

								rm.Content = ""

								rm.Files = append(rm.Files, &discordgo.File{
									Name:        "context.txt",
									ContentType: "text/plain",
									Reader:      strings.NewReader(content),
								})
							}

							if bm[i].Message.TTS && utils.CheckPermission(perms, discordgo.PermissionSendTTSMessages) {
								rm.TTS = true
							}

							// If no content, embeds or components, continue
							if rm.Content == "" && len(rm.Files) == 0 && len(rm.Embeds) == 0 && len(rm.Components) == 0 {
								continue
							}

							//l.Info("Sending backed up messages", zap.String("channel_id", restoredChannelId), zap.Int("i", i))

							_, err := discord.WebhookExecute(prevState.WebhookID, prevState.WebhookToken, false, &rm, discordgo.WithContext(ctx))

							if err != nil {
								if t.Options.IgnoreRestoreErrors {
									l.Warn("Failed to send message", zap.Error(err))
									continue
								}

								return nil, nil, fmt.Errorf("failed to send message: %w", err)
							}

							prevState.DoneChannels[restoredChannelId] = append(prevState.DoneChannels[restoredChannelId], bm[i].Message.ID)

							// Save intermediare result of sending the message to allow better resumability
							err = common.SaveIntermediateResult(progstate, progress, map[string]any{
								"doneChannels":        prevState.DoneChannels,
								"webhook_id":          prevState.WebhookID,
								"webhook_token":       prevState.WebhookToken,
								"restoredChannelsMap": restoredChannelsMap,
							})

							if err != nil {
								return nil, nil, fmt.Errorf("failed to save intermediate result: %w", err)
							}

							totalMessages++

							time.Sleep(time.Duration(t.Constraints.Restore.SendMessageSleep))

						}
					}
				}

				return nil, nil, nil
			},
		},
	).Exec(
		t,
		l,
		state,
		progstate,
	)

	if err != nil {
		l.Error("Failed to restore server", zap.Error(err))
		return nil, err
	}

	l.Info("Server restore complete")
	return outp, nil
}

func (t *ServerBackupRestore) Name() string {
	return "guild_restore_backup"
}

func (t *ServerBackupRestore) LocalPresets() *interfaces.PresetInfo {
	return &interfaces.PresetInfo{
		Runnable: true,
		Preset: &ServerBackupRestore{
			Constraints: FreePlanBackupConstraints,
			Options: BackupRestoreOpts{
				IgnoreRestoreErrors: false,
				ProtectedChannels:   []string{},
				ProtectedRoles:      []string{},
				BackupSource:        "{{.Args.BackupSource}}",
				Decrypt:             "{{.Settings.BackupPassword}}",
				ChannelRestoreMode:  ChannelRestoreModeFull,
			},
		},
		Comments: map[string]string{
			"Constraints.MaxServerBackups":      "Only 1 backup job should be running at any given time locally",
			"Constraints.FileType":              "The file type of the backup, you probably don't want to change this",
			"Constraints.Restore.MaxBodySize":   "Since this is a local job, we can afford to be more generous",
			"Options.IgnoreMessageBackupErrors": "We likely don't want errors ignored in local jobs",
			"Options.ProtectedChannels":         "Edit this to protect channels from being deleted",
			"Options.ProtectedRoles":            "Edit this to protect roles from being deleted",
			"Options.Decrypt":                   "The decryption key",
			"Options.ChannelRestoreMode":        "Should be full unless you know what you're doing",
			"Options.RoleRestoreMode":           "Should be full unless you know what you're doing",
		},
	}
}
