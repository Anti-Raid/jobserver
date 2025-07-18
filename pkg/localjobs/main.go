package localjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"text/template"

	jobs "github.com/Anti-Raid/jobserver/jobs"
	"github.com/Anti-Raid/jobserver/pkg/localjobs/easyconfig"
	"github.com/Anti-Raid/jobserver/pkg/localjobs/lib"
	"github.com/Anti-Raid/jobserver/pkg/localjobs/ljstate"
	"github.com/anti-raid/eureka/crypto"
	"github.com/anti-raid/eureka/snippets"
	"github.com/anti-raid/shellcli/cmd"
	"github.com/bwmarrin/discordgo"
	orderedmap "github.com/wk8/go-ordered-map/v2"
	"gopkg.in/yaml.v3"
)

var prefixDir = "ljconfig"

type fieldFlags map[string]string

func (i *fieldFlags) String() string {
	return "my string representation"
}

func (i *fieldFlags) Set(value string) error {
	valueSplit := strings.SplitN(value, "=", 2)
	if len(valueSplit) != 2 {
		return errors.New("all flags must be of form key=value")
	}

	if *i == nil {
		*i = make(map[string]string)
	}

	(*i)[valueSplit[0]] = valueSplit[1]
	return nil
}

var flags fieldFlags

func StartLocalJobs() {
	logger := snippets.CreateZap()

	err := os.MkdirAll(prefixDir, 0755)

	if err != nil {
		fmt.Println("ERROR: Failed to create localjobs directory:", err.Error())
		os.Exit(1)
	}

	f, err := os.Open(prefixDir + "/localjobs-config.yaml")

	if errors.Is(err, os.ErrNotExist) {
		// No config, trigger EasyConfig
		c, err := easyconfig.EasyConfig()

		if err != nil {
			fmt.Println("ERROR: Failed to create config:", err.Error())
			os.Exit(1)
		}

		f, err = os.Create(prefixDir + "/localjobs-config.yaml")

		if err != nil {
			fmt.Println("ERROR: Failed to create config:", err.Error())
			os.Exit(1)
		}

		err = yaml.NewEncoder(f).Encode(c)

		if err != nil {
			fmt.Println("ERROR: Failed to encode config:", err.Error())
			os.Exit(1)
		}

		ljstate.Config = c

		err = f.Close()

		if err != nil {
			fmt.Println("ERROR: Failed to close config:", err.Error())
		}
	} else {
		// Config exists, load it
		err = yaml.NewDecoder(f).Decode(&ljstate.Config)

		if err != nil {
			fmt.Println("ERROR: Failed to decode config:", err.Error())
			os.Exit(1)
		}

		err = f.Close()

		if err != nil {
			fmt.Println("ERROR: Failed to close config:", err.Error())
		}
	}

	// Unravel presets to presets directory if not found
	s, err := os.Stat(prefixDir + "/presets")

	if err == nil && !s.IsDir() {
		err = os.Remove(prefixDir + "/presets")

		if err != nil {
			fmt.Println("ERROR: Failed to remove presets file:", err.Error())
			os.Exit(1)
		}
	}

	if errors.Is(err, os.ErrNotExist) {
		err = os.Mkdir(prefixDir+"/presets", 0755)

		if err != nil {
			fmt.Println("ERROR: Failed to create presets directory:", err.Error())
			os.Exit(1)
		}
	}

	for _, task := range jobs.JobImplRegistry {
		// Stat localjobs/presets/preset.Name()
		s, err := os.Stat(prefixDir + "/presets/" + task.Name() + ".yaml")

		var isNotExist bool
		if err == nil && s.IsDir() {
			fmt.Println("INFO: Removing invalid preset", task.Name()+".yaml")
			err = os.Remove(prefixDir + "/presets/" + task.Name() + ".yaml")

			if err != nil {
				fmt.Println("ERROR: Failed to remove preset:", err.Error())
				os.Exit(1)
			}

			isNotExist = true
		} else if errors.Is(err, os.ErrNotExist) {
			isNotExist = true
		} else if err != nil {
			fmt.Println("ERROR: Failed to stat preset:", err.Error())
			os.Exit(1)
		}

		if !isNotExist {
			continue
		}

		// Create preset now from task.LocalPreset
		localPresets := task.LocalPresets()

		// WARN if not runnable
		if !localPresets.Runnable {
			fmt.Println("WARNING: Job", task.Name(), "is not officially runnable yet")
		}

		// Because yaml doesnt properly handle presets, we have to use reflection to convert it to a map then yaml it
		var pMap = orderedmap.New[string, any]()

		// This is hacky, but it works, first use encoding/json marshal/unmarshal and then yaml it
		pBytes, err := json.Marshal(localPresets.Preset)

		if err != nil {
			fmt.Println("ERROR: Failed to encode stage 0 preset:", err.Error())
			os.Exit(1)
		}

		err = json.Unmarshal(pBytes, &pMap)

		if err != nil {
			fmt.Println("ERROR: Failed to decode stage 0 preset:", err.Error())
			os.Exit(1)
		}

		// YAML unmarshal to preset file
		pBytes, err = yaml.Marshal(pMap)

		if err != nil {
			fmt.Println("ERROR: Failed to generate preset YAML content:", err.Error())
			os.Exit(1)
		}

		f, err := os.Create(prefixDir + "/presets/" + task.Name() + ".yaml")

		if err != nil {
			fmt.Println("ERROR: Failed to create preset:", err.Error())
			os.Exit(1)
		}

		// TODO: Make this look better in the actual file
		//
		// First write the comments in alphabetical order
		var commentKeysAlpha []string
		for key := range localPresets.Comments {
			commentKeysAlpha = append(commentKeysAlpha, key)
		}

		// Sort the keys
		slices.Sort(commentKeysAlpha)

		for _, key := range commentKeysAlpha {
			_, err = f.WriteString("# " + key + ": " + localPresets.Comments[key] + "\n")

			if err != nil {
				fmt.Println("ERROR: Failed to write preset:", err.Error())
				os.Exit(1)
			}
		}

		// One extra newline
		_, err = f.WriteString("\n")

		if err != nil {
			fmt.Println("ERROR: Failed to write preset:", err.Error())
			os.Exit(1)
		}

		// Now write the actual preset
		_, err = f.Write(pBytes)

		if err != nil {
			fmt.Println("ERROR: Failed to write preset:", err.Error())
			os.Exit(1)
		}

		fmt.Printf("INFO: Wrote preset %s (%d bytes [preset])\n", task.Name(), len(pBytes))

		err = f.Close()

		if err != nil {
			fmt.Println("ERROR: Failed to close preset:", err.Error())
		}
	}

	discordSess, err := discordgo.New("Bot " + ljstate.Config.BotToken)

	if err != nil {
		fmt.Println("ERROR: Failed to create Discord session:", err.Error())
		os.Exit(1)
	}

	botUser, err := discordSess.User("@me")

	if err != nil {
		fmt.Println("ERROR: Failed to get bot user:", err.Error())
		os.Exit(1)
	}

	var ctx context.Context
	var cancelFunc context.CancelFunc

	if os.Getenv("CTX_TIMEOUT") != "" {
		// Parse timeout from env
		dur, err := strconv.Atoi(os.Getenv("CTX_TIMEOUT"))

		if err != nil {
			fmt.Println("ERROR: Failed to parse CTX_TIMEOUT:", err.Error())
			os.Exit(1)
		}

		ctx, cancelFunc = context.WithTimeout(context.Background(), time.Duration(dur))
	} else {
		ctx, cancelFunc = context.WithCancel(context.Background())
	}

	if len(os.Args) == 0 {
		fmt.Println("ERROR: No command specified!")
		os.Exit(1)
	}

	state := lib.State{
		GuildId:     "localjobs",
		DiscordSess: discordSess,
		BotUser:     botUser,
		DebugInfoData: func() *debug.BuildInfo {
			bi, ok := debug.ReadBuildInfo()

			if !ok {
				return nil
			}

			return bi
		}(),
		ContextUse: ctx,
	}

	cmds := cmd.CommandLineState{
		Commands: map[string]cmd.Command{
			"runtask": {
				Help:    "Runs a task locally. Use --usage to view usage",
				Usage:   "runtask <task> [flags]",
				Example: "runtask guild_create_backup --field ServerID=1234567890",
				Func: func(progname string, args []string) {
					currArgs := os.Args

					os.Args = []string{progname}
					os.Args = append(os.Args, args...)
					// Flag parsing
					var usage bool
					var name string
					flag.BoolVar(&usage, "usage", false, "Show help")
					flag.StringVar(&name, "task", "", "The task to run")
					flag.Var(&flags, "field", "The fields to use")
					flag.Var(&flags, "F", "The fields to use [alias to field]")
					flag.Parse()

					os.Args = currArgs

					if usage {
						fmt.Printf("Usage: %s\n", "runtask <task> [flags]")
						fmt.Println("Flags:")
						flag.Usage()
						os.Exit(1)
					}

					if name == "" {
						fmt.Println("ERROR: No task specified!")
						flag.Usage()
						os.Exit(1)
					}

					fmt.Println("Flags:", flags)
					fmt.Println("Name:", name)

					// Find in task registry
					taskDef, ok := jobs.JobImplRegistry[name]

					if !ok {
						fmt.Println("ERROR: Job not found!")
						os.Exit(1)
					}

					ljstate.Config.Args = flags

					// Find preset file
					fi, err := os.Stat(prefixDir + "/presets/" + name + ".yaml")

					if errors.Is(err, os.ErrNotExist) {
						fmt.Println("WARNING: Preset not found despite task existing!")
					} else if err != nil {
						fmt.Println("ERROR: Failed to open preset:", err.Error())
						os.Exit(1)
					} else {
						if fi.IsDir() {
							fmt.Println("ERROR: Preset is a directory!")
							os.Exit(1)
						}

						// First text/template it
						templ, err := template.ParseFiles(prefixDir + "/presets/" + name + ".yaml")

						if err != nil {
							fmt.Println("ERROR: Failed to parse preset:", err.Error())
							os.Exit(1)
						}

						var buf bytes.Buffer

						err = templ.Option("missingkey=error").Execute(&buf, ljstate.Config)

						if err != nil {
							fmt.Println("ERROR: Failed to execute preset:", err.Error())
							os.Exit(1)
						}

						fmt.Println("Preset:", buf.String())

						// Preset found, decode it, this is a hack
						var m map[string]any
						err = yaml.NewDecoder(&buf).Decode(&m)

						if err != nil {
							fmt.Println("ERROR: Failed to decode preset:", err.Error())
							os.Exit(1)
						}

						// Now JSON encode it
						pBytes, err := json.Marshal(m)

						if err != nil {
							fmt.Println("ERROR: Failed to encode preset:", err.Error())
							os.Exit(1)
						}

						// Now decode it into the task
						var taskDefFilled = taskDef

						err = json.Unmarshal(pBytes, &taskDefFilled)

						if err != nil {
							fmt.Println("ERROR: Failed to decode preset:", err.Error())
							os.Exit(1)
						}

						taskDef = taskDefFilled
					}

					taskId := "local-" + crypto.RandString(32)

					fmt.Println("ID:", taskId)

					l, _ := lib.NewLocalLogger(taskId, logger)

					go func() {
						for {
							select {
							case <-ctx.Done():
								fmt.Println("ERROR: Context cancelled, exiting!")
								cancelFunc() // Just in case
								os.Exit(1)
							case <-time.After(time.Second * 30):
								fmt.Println("REMINDER: Job state is currently:", state)
							}
						}
					}()

					err = lib.ExecuteJobLocal(prefixDir, taskId, l, taskDef, lib.TaskLocalOpts{
						OnStateChange: func(state string) error {
							fmt.Println("INFO: Job state has changed to:", state)
							return nil
						},
					}, state)

					if err != nil {
						fmt.Println("ERROR: Failed to execute job:", err.Error())
						os.Exit(1)
					}
				},
			},
		},
		GetHeader: func() string {
			return fmt.Sprintf("localjobs %s", cmd.GetGitCommit())
		},
	}

	cmds.Run()
}
