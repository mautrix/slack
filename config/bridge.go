// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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

package config

import (
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/slack-go/slack"

	"maunium.net/go/mautrix/bridge/bridgeconfig"

	"go.mau.fi/mautrix-slack/database"
)

type IncrementalConfig struct {
	MessagesPerBatch int         `yaml:"messages_per_batch"`
	PostBatchDelay   int         `yaml:"post_batch_delay"`
	MaxMessages      MaxMessages `yaml:"max_messages"`
}

type MaxMessages struct {
	Channel int `yaml:"channel"`
	GroupDm int `yaml:"group_dm"`
	Dm      int `yaml:"dm"`
}

func (mb *MaxMessages) GetMaxMessagesFor(t database.ChannelType) int {
	switch t {
	case database.ChannelTypeChannel:
		return mb.Channel
	case database.ChannelTypeGroupDM:
		return mb.GroupDm
	case database.ChannelTypeDM:
		return mb.Dm
	default:
		return 0
	}
}

type BridgeConfig struct {
	UsernameTemplate      string `yaml:"username_template"`
	DisplaynameTemplate   string `yaml:"displayname_template"`
	ChannelNameTemplate   string `yaml:"channel_name_template"`
	TeamNameTemplate      string `yaml:"team_name_template"`
	PrivateChatPortalMeta string `yaml:"private_chat_portal_meta"`

	CommandPrefix string `yaml:"command_prefix"`

	DeliveryReceipts            bool `yaml:"delivery_receipts"`
	ResendBridgeInfo            bool `yaml:"resend_bridge_info"`
	MessageStatusEvents         bool `yaml:"message_status_events"`
	MessageErrorNotices         bool `yaml:"message_error_notices"`
	CustomEmojiReactions        bool `yaml:"custom_emoji_reactions"`
	KickOnLogout                bool `yaml:"kick_on_logout"`
	WorkspaceAvatarInRooms      bool `yaml:"workspace_avatar_in_rooms"`
	ParticipantSyncCount        int  `yaml:"participant_sync_count"`
	ParticipantSyncOnlyOnCreate bool `yaml:"participant_sync_only_on_create"`

	ManagementRoomText bridgeconfig.ManagementRoomTexts `yaml:"management_room_text"`

	PortalMessageBuffer int `yaml:"portal_message_buffer"`

	SyncDirectChatList    bool `yaml:"sync_direct_chat_list"`
	FederateRooms         bool `yaml:"federate_rooms"`
	DefaultBridgeReceipts bool `yaml:"default_bridge_receipts"`
	DefaultBridgePresence bool `yaml:"default_bridge_presence"`

	DoublePuppetConfig bridgeconfig.DoublePuppetConfig `yaml:",inline"`

	Encryption bridgeconfig.EncryptionConfig `yaml:"encryption"`

	Provisioning struct {
		Prefix       string `yaml:"prefix"`
		SharedSecret string `yaml:"shared_secret"`
	} `yaml:"provisioning"`

	Permissions bridgeconfig.PermissionConfig `yaml:"permissions"`

	Backfill struct {
		Enable bool `yaml:"enable"`

		ConversationsCount int `yaml:"conversations_count"`

		UnreadHoursThreshold int `yaml:"unread_hours_threshold"`

		ImmediateMessages int `yaml:"immediate_messages"`

		Incremental IncrementalConfig `yaml:"incremental"`
	} `yaml:"backfill"`

	usernameTemplate       *template.Template `yaml:"-"`
	displaynameTemplate    *template.Template `yaml:"-"`
	botDisplaynameTemplate *template.Template `yaml:"-"`
	channelNameTemplate    *template.Template `yaml:"-"`
	teamNameTemplate       *template.Template `yaml:"-"`
}

type umBridgeConfig BridgeConfig

func (bc *BridgeConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal((*umBridgeConfig)(bc))
	if err != nil {
		return err
	}

	bc.usernameTemplate, err = template.New("username").Parse(bc.UsernameTemplate)
	if err != nil {
		return err
	} else if !strings.Contains(bc.FormatUsername("1234567890"), "1234567890") {
		return fmt.Errorf("username template is missing user ID placeholder")
	}

	bc.displaynameTemplate, err = template.New("displayname").Parse(bc.DisplaynameTemplate)
	if err != nil {
		return err
	}

	bc.channelNameTemplate, err = template.New("channel_name").Parse(bc.ChannelNameTemplate)
	if err != nil {
		return err
	}

	bc.teamNameTemplate, err = template.New("team_name").Parse(bc.TeamNameTemplate)
	if err != nil {
		return err
	}

	return nil
}

var _ bridgeconfig.BridgeConfig = (*BridgeConfig)(nil)

func (bc *BridgeConfig) GetEncryptionConfig() bridgeconfig.EncryptionConfig {
	return bc.Encryption
}

func (bc *BridgeConfig) GetCommandPrefix() string {
	return bc.CommandPrefix
}

func (bc *BridgeConfig) GetManagementRoomTexts() bridgeconfig.ManagementRoomTexts {
	return bc.ManagementRoomText
}

func (bc *BridgeConfig) GetDoublePuppetConfig() bridgeconfig.DoublePuppetConfig {
	return bc.DoublePuppetConfig
}

func (bc *BridgeConfig) FormatUsername(userID string) string {
	var buffer strings.Builder
	_ = bc.usernameTemplate.Execute(&buffer, userID)
	return buffer.String()
}

func (bc *BridgeConfig) FormatDisplayname(user *slack.User) string {
	var buffer strings.Builder
	_ = bc.displaynameTemplate.Execute(&buffer, user)
	return buffer.String()
}

func (bc *BridgeConfig) FormatBotDisplayname(bot *slack.Bot) string {
	return bc.FormatDisplayname(&slack.User{
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
	Type       database.ChannelType
	TeamName   string
	TeamDomain string
}

func (bc *BridgeConfig) FormatChannelName(params ChannelNameParams) string {
	var buffer strings.Builder
	_ = bc.channelNameTemplate.Execute(&buffer, params)
	return buffer.String()
}

func (bc *BridgeConfig) FormatTeamName(params *slack.TeamInfo) string {
	var buffer strings.Builder
	_ = bc.teamNameTemplate.Execute(&buffer, params)
	return buffer.String()
}

func (bc *BridgeConfig) GetResendBridgeInfo() bool {
	return bc.ResendBridgeInfo
}

func (bc *BridgeConfig) EnableMessageStatusEvents() bool {
	return bc.MessageStatusEvents
}

func (bc *BridgeConfig) EnableMessageErrorNotices() bool {
	return bc.MessageErrorNotices
}

func boolToInt(val bool) int {
	if val {
		return 1
	}
	return 0
}

func (bc *BridgeConfig) Validate() error {
	_, hasWildcard := bc.Permissions["*"]
	_, hasExampleDomain := bc.Permissions["example.com"]
	_, hasExampleUser := bc.Permissions["@admin:example.com"]
	exampleLen := boolToInt(hasWildcard) + boolToInt(hasExampleUser) + boolToInt(hasExampleDomain)
	if len(bc.Permissions) <= exampleLen {
		return errors.New("bridge.permissions not configured")
	}
	return nil
}
