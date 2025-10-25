package main

import "github.com/bwmarrin/discordgo"

var (
	idMinValue               = 1.0
	idMaxValue               = 9007199254740991.0
	wipeCutoffMinValue       = 1.0
	wipeCutoffMaxValue       = 50.0
	adminPerms         int64 = discordgo.PermissionAdministrator
	commands                 = []*discordgo.ApplicationCommand{
		{
			Name:        "set-config",
			Description: "Set bot configuration",
			DescriptionLocalizations: &map[discordgo.Locale]string{
				discordgo.Russian: "Настройка бота",
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type: discordgo.ApplicationCommandOptionChannel,
					Name: "channel",
					NameLocalizations: map[discordgo.Locale]string{
						discordgo.Russian: "канал",
					},
					Description: "Text channel for notifications",
					DescriptionLocalizations: map[discordgo.Locale]string{
						discordgo.Russian: "Текстовый канал для уведомлений",
					},
					Required: true,
					ChannelTypes: []discordgo.ChannelType{
						discordgo.ChannelTypeGuildText,
					},
				},
				{
					Type: discordgo.ApplicationCommandOptionInteger,
					Name: "guild_id",
					NameLocalizations: map[discordgo.Locale]string{
						discordgo.Russian: "идентификатор_гильдии",
					},
					Description: "Guild id from warcraftlogs.com",
					DescriptionLocalizations: map[discordgo.Locale]string{
						discordgo.Russian: "Идентификатор гильдии на warcraftlogs.com",
					},
					Required: true,
					MinValue: &idMinValue,
					MaxValue: idMaxValue,
				},
				{
					Type: discordgo.ApplicationCommandOptionInteger,
					Name: "wipe_cutoff",
					NameLocalizations: map[discordgo.Locale]string{
						discordgo.Russian: "wipe_cutoff",
					},
					Description: "The number of deaths after which all subsequent events should be ignored",
					DescriptionLocalizations: map[discordgo.Locale]string{
						discordgo.Russian: "Количество смертей, после которого все последующие события игнорируются",
					},
					Required: true,
					MinValue: &wipeCutoffMinValue,
					MaxValue: wipeCutoffMaxValue,
				},
			},
			DefaultMemberPermissions: &adminPerms,
			Contexts:                 &[]discordgo.InteractionContextType{discordgo.InteractionContextGuild},
		},
		{
			Name:        "get-config",
			Description: "Show current configuration",
			DescriptionLocalizations: &map[discordgo.Locale]string{
				discordgo.Russian: "Посмотреть текущие настройки",
			},
			Options:                  []*discordgo.ApplicationCommandOption{},
			DefaultMemberPermissions: &adminPerms,
			Contexts:                 &[]discordgo.InteractionContextType{discordgo.InteractionContextGuild},
		},
	}
)

var commandNames = func() []string {
	names := make([]string, len(commands))
	for i, cmd := range commands {
		names[i] = cmd.Name
	}
	return names
}()
