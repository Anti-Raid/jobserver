package state

import (
	"context"
	"net/http"
	"os"
	"runtime/debug"
	"strings"

	"github.com/Anti-Raid/jobserver/config"
	"github.com/Anti-Raid/jobserver/objectstorage"
	"github.com/anti-raid/eureka/genconfig"
	"github.com/anti-raid/eureka/proxy"
	"github.com/anti-raid/eureka/snippets"
	"github.com/bwmarrin/discordgo"
	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

var (
	Context              = context.Background()
	Config               *config.Config
	Validator            = validator.New()
	BotUser              *discordgo.User
	ObjectStorage        *objectstorage.ObjectStorage
	CurrentOperationMode string // Current mode splashtail is operating in

	// Debug stuff
	BuildInfo  *debug.BuildInfo
	ExtraDebug ExtraDebugInfo

	Pool    *pgxpool.Pool
	Discord *discordgo.Session
	Logger  *zap.Logger
)

type ExtraDebugInfo struct {
	VSC         string
	VSCRevision string
}

func SetupDebug() {
	var ok bool
	BuildInfo, ok = debug.ReadBuildInfo()

	if !ok {
		panic("failed to read build info")
	}

	// Get vcs.revision
	for _, d := range BuildInfo.Settings {
		if d.Key == "vcs" {
			ExtraDebug.VSC = d.Value
		}
		if d.Key == "vcs.revision" {
			ExtraDebug.VSCRevision = d.Value
		}
	}
}

func SetupBase() {
	genconfig.GenConfig(config.Config{})

	cfg, err := os.ReadFile("config.yaml")

	if err != nil {
		panic(err)
	}

	err = yaml.Unmarshal(cfg, &Config)

	if err != nil {
		panic(err)
	}

	err = Validator.Struct(Config)

	if err != nil {
		panic("configError: " + err.Error())
	}

	Logger = snippets.CreateZap()

	// Discordgo
	Discord, err = discordgo.New("Bot " + Config.DiscordAuth.Token)

	if err != nil {
		panic(err)
	}

	Discord.Client.Transport = proxy.NewHostRewriter(strings.Replace(Config.Meta.Proxy, "http://", "", 1), http.DefaultTransport, func(s string) {
		Logger.Info("[PROXY]", zap.String("note", s))
	})

}

func Setup() {
	SetupDebug()
	SetupBase()

	var err error

	// Postgres
	Pool, err = pgxpool.New(Context, Config.Meta.PostgresURL)

	if err != nil {
		panic(err)
	}

	// Object Storage
	ObjectStorage, err = objectstorage.New(&Config.ObjectStorage)

	if err != nil {
		panic(err)
	}

	// Discordgo
	Discord, err = discordgo.New("Bot " + Config.DiscordAuth.Token)

	if err != nil {
		panic(err)
	}

	Discord.Client.Transport = proxy.NewHostRewriter(strings.Replace(Config.Meta.Proxy, "http://", "", 1), http.DefaultTransport, func(s string) {
		Logger.Info("[PROXY]", zap.String("note", s))
	})

	// Verify token
	bu, err := Discord.User("@me")

	if err != nil {
		panic(err)
	}

	BotUser = bu

	Discord.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		Logger.Info("[DISCORD]", zap.String("note", "ready"))
	})
}
