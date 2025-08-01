package backups

import (
	"bytes"

	"github.com/Anti-Raid/jobserver/utils/timex"
	iblfile "github.com/anti-raid/iblfile/go"
	"github.com/bwmarrin/discordgo"
)

type BackupCreateConstraints struct {
	TotalMaxMessages          int // The maximum number of messages to backup
	MinPerChannel             int // The minimum number of messages per channel
	DefaultPerChannel         int // The default number of messages per channel
	JpegReencodeQuality       int // The quality to use when reencoding to JPEGs
	GuildAssetReencodeQuality int // The quality to use when reencoding guild assets
}

type BackupRestoreConstraints struct {
	RoleDeleteSleep    timex.Duration // How long to sleep between role deletes
	RoleCreateSleep    timex.Duration // How long to sleep between role creates
	ChannelDeleteSleep timex.Duration // How long to sleep between channel deletes
	ChannelCreateSleep timex.Duration // How long to sleep between channel creates
	ChannelEditSleep   timex.Duration // How long to sleep between channel edits
	SendMessageSleep   timex.Duration // How long to sleep between message sends
	HttpClientTimeout  timex.Duration // How long to wait for HTTP requests to complete
	MaxBodySize        int64          // The maximum size of the backup file to download/use
}

type BackupConstraints struct {
	Create           *BackupCreateConstraints
	Restore          *BackupRestoreConstraints
	MaxServerBackups int    // How many backup/restore jobs can run concurrently per server
	FileType         string // The file type to use for backups
}

var FreePlanBackupConstraints = &BackupConstraints{
	Create: &BackupCreateConstraints{
		TotalMaxMessages:          1000,
		MinPerChannel:             1,
		DefaultPerChannel:         100,
		JpegReencodeQuality:       75,
		GuildAssetReencodeQuality: 85,
	},
	Restore: &BackupRestoreConstraints{
		RoleDeleteSleep:    1 * timex.Second,
		RoleCreateSleep:    2 * timex.Second,
		ChannelDeleteSleep: 500 * timex.Millisecond,
		ChannelCreateSleep: 500 * timex.Millisecond,
		ChannelEditSleep:   1 * timex.Second,
		SendMessageSleep:   350 * timex.Millisecond,
		HttpClientTimeout:  10 * timex.Second,
		MaxBodySize:        250_000_000, // 100MB
	},
	MaxServerBackups: 1,
	FileType:         "backup.server",
}

var allowedChannelTypes = []discordgo.ChannelType{
	discordgo.ChannelTypeGuildText,
	discordgo.ChannelTypeGuildNews,
	discordgo.ChannelTypeGuildNewsThread,
	discordgo.ChannelTypeGuildPublicThread,
	discordgo.ChannelTypeGuildPrivateThread,
	discordgo.ChannelTypeGuildForum,
}

type ChannelRestoreMode string

const (
	ChannelRestoreModeFull           ChannelRestoreMode = "full"
	ChannelRestoreModeDiff           ChannelRestoreMode = "diff" // TODO
	ChannelRestoreModeIgnoreExisting ChannelRestoreMode = "ignore_existing"
)

// Options that can be set when creatng a backup
type BackupCreateOpts struct {
	Channels                  []string       `description:"If set, the channels to prune messages from"`
	PerChannel                int            `description:"The number of messages per channel"`
	MaxMessages               int            `description:"The maximum number of messages to backup"`
	BackupMessages            bool           `description:"Whether to backup messages or not"`
	BackupGuildAssets         []string       `description:"What assets to back up"`
	IgnoreMessageBackupErrors bool           `description:"Whether to ignore errors while backing up messages or not and skip these channels"`
	RolloverLeftovers         bool           `description:"Whether to attempt rollover of leftover message quota to another channels or not"`
	SpecialAllocations        map[string]int `description:"Specific channel allocation overrides"`
	Encrypt                   string         `description:"The key to encrypt backups with, if any"`
}

// Options that can be set when restoring a backup
type BackupRestoreOpts struct {
	IgnoreRestoreErrors bool               `description:"Whether to ignore errors while restoring or not and skip these channels/roles"`
	ProtectedChannels   []string           `description:"Channels to protect from being deleted"`
	ProtectedRoles      []string           `description:"Roles to protect from being deleted"`
	BackupSource        string             `description:"The source of the backup"`
	Decrypt             string             `description:"The key to decrypt backups with, if any"`
	ChannelRestoreMode  ChannelRestoreMode `description:"Channel backup restore method. Use 'full' if unsure"`
}

// Represents a backed up message
type BackupMessage struct {
	Message *discordgo.Message `json:"message"`
}

func init() {
	iblfile.RegisterFormat("backup", &iblfile.Format{
		Format:  "server",
		Version: "a1",
		GetExtended: func(section map[string]*bytes.Buffer, meta *iblfile.Meta) (map[string]any, error) {
			return map[string]any{}, nil
		},
	})
}
