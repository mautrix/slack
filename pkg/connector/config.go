// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	_ "embed"
	"strings"
	"text/template"

	"github.com/slack-go/slack"
	up "go.mau.fi/util/configupgrade"
	"gopkg.in/yaml.v3"
)

//go:embed example-config.yaml
var ExampleConfig string

type Config struct {
	DisplaynameTemplate string `yaml:"displayname_template"`
	ChannelNameTemplate string `yaml:"channel_name_template"`
	TeamNameTemplate    string `yaml:"team_name_template"`

	CustomEmojiReactions        bool `yaml:"custom_emoji_reactions"`
	WorkspaceAvatarInRooms      bool `yaml:"workspace_avatar_in_rooms"`
	ParticipantSyncCount        int  `yaml:"participant_sync_count"`
	ParticipantSyncOnlyOnCreate bool `yaml:"participant_sync_only_on_create"`

	Backfill BackfillConfig `yaml:"backfill"`

	displaynameTemplate *template.Template `yaml:"-"`
	channelNameTemplate *template.Template `yaml:"-"`
	teamNameTemplate    *template.Template `yaml:"-"`
}

type BackfillConfig struct {
	ConversationCount int  `yaml:"conversation_count"`
	Enabled           bool `yaml:"enabled"`
}

type umConfig Config

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	err := node.Decode((*umConfig)(c))
	if err != nil {
		return err
	}

	c.displaynameTemplate, err = template.New("displayname").Parse(c.DisplaynameTemplate)
	if err != nil {
		return err
	}
	c.channelNameTemplate, err = template.New("channel_name").Parse(c.ChannelNameTemplate)
	if err != nil {
		return err
	}
	c.teamNameTemplate, err = template.New("team_name").Parse(c.TeamNameTemplate)
	if err != nil {
		return err
	}
	return nil
}

func executeTemplate(tpl *template.Template, data any) string {
	var buffer strings.Builder
	_ = tpl.Execute(&buffer, data)
	return buffer.String()
}

func (c *Config) FormatDisplayname(user *slack.User) string {
	return executeTemplate(c.displaynameTemplate, user)
}

func (c *Config) FormatBotDisplayname(bot *slack.Bot) string {
	return c.FormatDisplayname(&slack.User{
		ID:      bot.ID,
		Name:    bot.Name,
		IsBot:   true,
		Deleted: bot.Deleted,
		Updated: bot.Updated,
		Profile: slack.UserProfile{
			DisplayName: bot.Name,
		},
	})
}

type ChannelNameParams struct {
	*slack.Channel
	TeamName     string
	TeamDomain   string
	IsNoteToSelf bool
}

func (c *Config) FormatChannelName(params *ChannelNameParams) string {
	return executeTemplate(c.channelNameTemplate, params)
}

func (c *Config) FormatTeamName(params *slack.TeamInfo) string {
	return executeTemplate(c.teamNameTemplate, params)
}

func (s *SlackConnector) GetConfig() (example string, data any, upgrader up.Upgrader) {
	return ExampleConfig, &s.Config, up.SimpleUpgrader(upgradeConfig)
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "displayname_template")
	helper.Copy(up.Str, "channel_name_template")
	helper.Copy(up.Str, "team_name_template")
	helper.Copy(up.Bool, "custom_emoji_reactions")
	helper.Copy(up.Bool, "workspace_avatar_in_rooms")
	helper.Copy(up.Int, "participant_sync_count")
	helper.Copy(up.Bool, "participant_sync_only_on_create")
	helper.Copy(up.Int, "backfill", "conversation_count")
}
