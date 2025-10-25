package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"bot/storage"
	"bot/warcraftlogs"
	"bot/watcher"

	"github.com/bwmarrin/discordgo"
	"github.com/jellydator/ttlcache/v3"
	"github.com/kelseyhightower/envconfig"
	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"
)

type Config struct {
	DiscordBotToken string `envconfig:"DISCORD_BOT_TOKEN" required:"true"`
	WLClientId      string `envconfig:"WL_CLIENT_ID" required:"true"`
	WLClientSecret  string `envconfig:"WL_CLIENT_SECRET" required:"true"`
}

func main() {
	var config Config
	envconfig.MustProcess("", &config)

	zlogger, _ := zap.NewDevelopment()
	defer zlogger.Sync()
	slogger := slog.New(zapslog.NewHandler(zlogger.Core()))
	slog.SetDefault(slogger)

	db, err := bolt.Open("./store.db", 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		panic(err)
	}
	defer db.Close()
	storage.MustInitDB(db)
	store := storage.New(db)

	wlClient, err := warcraftlogs.NewClient(config.WLClientId, config.WLClientSecret)
	if err != nil {
		panic(err)
	}
	w := watcher.New(wlClient)

	token := "Bot " + config.DiscordBotToken
	dg, err := discordgo.New(token)
	if err != nil {
		panic(err)
	}

	messageCache := ttlcache.New[string, string](
		ttlcache.WithTTL[string, string](12 * time.Hour),
	)
	go messageCache.Start()

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		slog.Info("bot is online")
	})

	dg.AddHandler(func(s *discordgo.Session, g *discordgo.GuildCreate) {
		slog.Info("bot is connected to server", slog.String("server", g.Guild.ID), slog.String("server_name", g.Guild.Name))
		registerCommands(s, g.Guild)
		srv, err := store.ReadServer(g.Guild.ID)
		if err != nil {
			slog.Error("error loading server configuration", slog.String("server", g.Guild.ID), "error", err)
			return
		}
		if srv != nil {
			msgs, err := s.ChannelMessages(srv.ChannelId, 100, "", "", "")
			if err != nil {
				slog.Error("error loading message history", slog.String("server", g.Guild.ID), slog.String("channel", srv.ChannelId), "error", err)
			}
			for _, msg := range msgs {
				if msg.Author.ID != s.State.User.ID {
					continue
				}
				lastDate := msg.Timestamp
				if msg.EditedTimestamp != nil {
					lastDate = *msg.EditedTimestamp
				}
				if time.Since(lastDate) > 12*time.Hour {
					continue
				}

				url := msg.Embeds[0].URL
				idx := strings.LastIndex(url, "/")
				reportCode := url[idx+1:]

				key := srv.ServerId + srv.ChannelId + reportCode
				messageCache.Set(key, msg.ID, ttlcache.DefaultTTL)
			}
			slog.Info("starting watcher", slog.String("server", g.Guild.ID))
			w.Watch(*srv)
		}
	})

	dg.AddHandler(func(s *discordgo.Session, g *discordgo.GuildDelete) {
		slog.Info("bot is disconnected from server", slog.String("server", g.Guild.ID))
		store.DeleteServer(g.Guild.ID)
		w.Unwatch(g.Guild.ID)
	})

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}
		data := i.ApplicationCommandData()

		switch data.Name {
		case "set-config":
			channelId := data.Options[0].ChannelValue(s).ID
			wlGuildId := int64(data.Options[1].Value.(float64))
			wipeCutoff := int64(data.Options[2].Value.(float64))
			server := storage.Server{
				ServerId:   i.GuildID,
				ChannelId:  channelId,
				WlGuildId:  wlGuildId,
				WipeCutoff: wipeCutoff,
			}
			err := store.SaveServer(server)
			if err != nil {
				slog.Error("error saving configuration", slog.String("server", i.GuildID), "error", err)
				switch i.Locale {
				case discordgo.Russian:
					respond(s, i, "❌ Ошибка, попробуйте еще раз")
				default:
					respond(s, i, "❌ Error, try again")
				}
				return
			}
			slog.Info("stopping watcher", "server", server.ServerId)
			w.Unwatch(server.ServerId)
			slog.Info("starting watcher", "server", server.ServerId)
			w.Watch(server)
			slog.Info("bot is configured", slog.String("server", i.GuildID), slog.String("channelId", channelId), slog.Int64("wlGuildId", wlGuildId))
			switch i.Locale {
			case discordgo.Russian:
				respond(s, i, "✅ Бот настроен")
			default:
				respond(s, i, "✅ Bot is configured")
			}
		case "get-config":
			server, err := store.ReadServer(i.GuildID)
			if err != nil {
				slog.Error("error reading configuration", slog.String("server", i.GuildID), "error", err)
				switch i.Locale {
				case discordgo.Russian:
					respond(s, i, "❌ Ошибка, попробуйте еще раз")
				default:
					respond(s, i, "❌ Error, try again")
				}
				return
			}
			if server == nil {
				respond(s, i, "⚠️ Бот не настроен")
				return
			}
			switch i.Locale {
			case discordgo.Russian:
				respond(s, i, fmt.Sprintf(
					"💡 Канал для уведомлений: <#%v>\n💡 Идентификатор гильдии на warcraftlogs.com: %v\n💡 Wipe cutoff: %v",
					server.ChannelId, server.WlGuildId, server.WipeCutoff),
				)
			default:
				respond(s, i, fmt.Sprintf(
					"💡 Channel for notifications: <#%v>\n💡 Guild id from warcraftlogs.com: %v\n💡 Wipe cutoff: %v",
					server.ChannelId, server.WlGuildId, server.WipeCutoff),
				)
			}
		default:
			slog.Warn("unknown command, should remove it", slog.String("server", i.GuildID), slog.String("command", data.Name))
			switch i.Locale {
			case discordgo.Russian:
				respond(s, i, "⚠️ Неизвестная команда")
			default:
				respond(s, i, "⚠️ Unknown command")
			}
			removeCommand(s, i.GuildID, data)
		}
	})

	w.OnUpdate(func(se watcher.StatsEvent) {
		key := makeKey(se)
		embed := constructEmbed(se)

		item := messageCache.Get(key)

		if item != nil {
			_, err := dg.ChannelMessageEditComplex(&discordgo.MessageEdit{
				ID:      item.Value(),
				Channel: se.Server.ChannelId,
				Embeds:  &[]*discordgo.MessageEmbed{embed},
			})
			if err != nil {
				slog.Error("error updating message", slog.String("server", se.Server.ServerId), slog.String("channel", se.Server.ChannelId), "error", err)
			}
			return
		}

		msgOut, err := dg.ChannelMessageSendComplex(se.Server.ChannelId, &discordgo.MessageSend{
			Embeds: []*discordgo.MessageEmbed{embed},
		})
		if err != nil {
			slog.Error("error sending message", slog.String("server", se.Server.ServerId), slog.String("channel", se.Server.ChannelId), "error", err)
			return
		}
		messageCache.Set(key, msgOut.ID, ttlcache.DefaultTTL)
	})

	err = dg.Open()
	if err != nil {
		panic(err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	dg.Close()
}

func makeKey(se watcher.StatsEvent) string {
	return se.Server.ServerId + se.Server.ChannelId + se.ReportId
}

func respond(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   1 << 6, // ephemeral (видно только вызвавшему)
		},
	})
}

func registerCommands(s *discordgo.Session, guild *discordgo.Guild) {
	_, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, guild.ID, commands)
	if err != nil {
		slog.Error("error registered commands", slog.String("server", guild.ID), "commands", commandNames, "error", err)
		return
	}
	slog.Info("commands registered", slog.String("server", guild.ID), "commands", commandNames)
}

func removeCommand(s *discordgo.Session, guildId string, command discordgo.ApplicationCommandInteractionData) {
	err := s.ApplicationCommandDelete(s.State.User.ID, guildId, command.ID)
	if err != nil {
		slog.Error("error removing command", slog.String("server", guildId), slog.String("command", command.Name), "error", err)
		return
	}
	slog.Info("command removed", slog.String("server", guildId), slog.String("command", command.Name))
}

func constructEmbed(stats watcher.StatsEvent) *discordgo.MessageEmbed {
	color := 0x2ECC71
	if !stats.Live {
		color = 0x95A5A6
	}
	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("Warcraft Logs\n%v", stats.Title),
		Description: fmt.Sprintf("```Started by %v\non %v```", stats.StartedBy, stats.StartedAt.Format(time.DateTime)),
		URL:         stats.URL,
		Color:       color,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "Top First Deaths",
				Value:  formatTop(stats.TopFirstDeath),
				Inline: false,
			},
			{
				Name:   "Top Deaths Before Wipe",
				Value:  formatTop(stats.TopDeath),
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Last upload",
		},
		Timestamp: stats.LastUpload.Format(time.RFC3339),
	}
}

func formatTop(top []warcraftlogs.PlayerTop) string {
	if len(top) == 0 {
		return "``` ```"
	}
	var sb strings.Builder
	sb.Grow(128)
	sb.WriteString("```")
	for i, t := range top {
		sb.WriteString(padRight(t.Name, 12))
		sb.WriteString(padLeft(strconv.Itoa(t.Value), 12))
		if i != len(top)-1 {
			sb.WriteRune('\n')
		}
	}
	sb.WriteString("```")
	return sb.String()
}

func padRight(s string, to int) string {
	if diff := to - utf8.RuneCountInString(s); diff > 0 {
		return s + strings.Repeat(" ", diff)
	}
	return s
}

func padLeft(s string, to int) string {
	if diff := to - utf8.RuneCountInString(s); diff > 0 {
		return strings.Repeat(" ", diff) + s
	}
	return s
}
