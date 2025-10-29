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
	"context"
	"strconv"
	"time"

	"go.mau.fi/util/ffmpeg"
	"go.mau.fi/util/jsontime"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

func (s *SlackConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{
		// GetUserInfo has an internal rate limit of 1 fetch per 24 hours,
		// so we're fine to tell the bridge to fetch user info all the time.
		AggressiveUpdateInfo: true,
		Provisioning: bridgev2.ProvisioningCapabilities{
			ResolveIdentifier: bridgev2.ResolveIdentifierCapabilities{
				CreateDM:    true,
				LookupEmail: true,
				ContactList: false, // TODO allow fetching all users in a workspace? (requires pagination)
				Search:      true,
			},
			GroupCreation: map[string]bridgev2.GroupTypeCapabilities{
				"public-channel": {
					TypeDescription: "Public channel",
					Name:            bridgev2.GroupFieldCapability{Allowed: true, Required: true, MinLength: 1, MaxLength: 80},
					Topic:           bridgev2.GroupFieldCapability{Allowed: true},
				},
				"private-channel": {
					TypeDescription: "Private channel",
					Name:            bridgev2.GroupFieldCapability{Allowed: true, Required: true, MinLength: 1, MaxLength: 80},
					Topic:           bridgev2.GroupFieldCapability{Allowed: true},
				},
				"group": {
					TypeDescription: "Group DM",
					Participants:    bridgev2.GroupFieldCapability{Allowed: true, Required: true, MinLength: 2, MaxLength: 8},
					Topic:           bridgev2.GroupFieldCapability{Allowed: true},
				},
			},
		},
	}
}

func (s *SlackConnector) GetBridgeInfoVersion() (info, caps int) {
	return 1, 2
}

func supportedIfFFmpeg() event.CapabilitySupportLevel {
	if ffmpeg.Supported() {
		return event.CapLevelPartialSupport
	}
	return event.CapLevelRejected
}

func capID() string {
	base := "fi.mau.slack.capabilities.2025_10_29"
	if ffmpeg.Supported() {
		return base + "+ffmpeg"
	}
	return base
}

const MaxFileSize = 1 * 1000 * 1000 * 1000
const MaxTextLength = 40000

var roomCaps = &event.RoomFeatures{
	ID: capID(),
	Formatting: event.FormattingFeatureMap{
		event.FmtBold:               event.CapLevelFullySupported,
		event.FmtItalic:             event.CapLevelFullySupported,
		event.FmtStrikethrough:      event.CapLevelFullySupported,
		event.FmtInlineCode:         event.CapLevelFullySupported,
		event.FmtCodeBlock:          event.CapLevelFullySupported,
		event.FmtSyntaxHighlighting: event.CapLevelDropped,
		event.FmtBlockquote:         event.CapLevelFullySupported,
		event.FmtInlineLink:         event.CapLevelFullySupported,
		event.FmtUserLink:           event.CapLevelFullySupported,
		event.FmtRoomLink:           event.CapLevelFullySupported,
		event.FmtEventLink:          event.CapLevelUnsupported,
		event.FmtAtRoomMention:      event.CapLevelFullySupported,
		event.FmtUnorderedList:      event.CapLevelFullySupported,
		event.FmtOrderedList:        event.CapLevelFullySupported,
		event.FmtListStart:          event.CapLevelFullySupported,
		event.FmtListJumpValue:      event.CapLevelDropped,
		event.FmtCustomEmoji:        event.CapLevelFullySupported,
	},
	File: event.FileFeatureMap{
		event.MsgImage: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"image/jpeg": event.CapLevelFullySupported,
				"image/png":  event.CapLevelFullySupported,
				"image/gif":  event.CapLevelFullySupported,
				"image/webp": event.CapLevelFullySupported,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.MsgVideo: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"video/mp4":  event.CapLevelFullySupported,
				"video/webm": event.CapLevelFullySupported,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.MsgAudio: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"audio/mpeg": event.CapLevelFullySupported,
				"audio/webm": event.CapLevelFullySupported,
				"audio/wav":  event.CapLevelFullySupported,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.MsgFile: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				// TODO Slack Connect rejects some types
				// https://slack.com/intl/en-gb/help/articles/1500002249342-Restricted-file-types-in-Slack-Connect
				"*/*": event.CapLevelFullySupported,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.CapMsgGIF: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"image/gif": event.CapLevelFullySupported,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
		},
		event.CapMsgVoice: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"audio/ogg":               supportedIfFFmpeg(),
				"audio/webm; codecs=opus": event.CapLevelFullySupported,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          MaxFileSize,
			MaxDuration:      ptr.Ptr(jsontime.S(5 * time.Minute)),
		},
	},
	State: event.StateFeatureMap{
		event.StateRoomName.Type: {Level: event.CapLevelFullySupported},
		event.StateTopic.Type:    {Level: event.CapLevelFullySupported},
	},
	LocationMessage: event.CapLevelRejected,
	MaxTextLength:   MaxTextLength,
	Thread:          event.CapLevelFullySupported,
	Edit:            event.CapLevelFullySupported,
	EditMaxAge:      nil,
	Delete:          event.CapLevelFullySupported,
	Reaction:        event.CapLevelFullySupported,
}

var dmCaps *event.RoomFeatures

func init() {
	dmCaps = ptr.Clone(roomCaps)
	dmCaps.ID += "+dm"
	dmCaps.State = event.StateFeatureMap{
		// Weirdly enough, DMs and group DMs allow topics on Slack
		event.StateTopic.Type: {Level: event.CapLevelFullySupported},
	}
}

func (s *SlackClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	meta := &slackid.PortalMetadata{}
	topLevel := portal.GetTopLevelParent()
	if topLevel != nil {
		meta = topLevel.Metadata.(*slackid.PortalMetadata)
	}
	caps := roomCaps
	if portal.RoomType == database.RoomTypeDM || portal.RoomType == database.RoomTypeGroupDM {
		caps = dmCaps
	}
	if meta.EditMaxAge != nil && *meta.EditMaxAge >= 0 {
		caps = ptr.Clone(roomCaps)
		caps.ID += "+edit_max_age=" + strconv.Itoa(*meta.EditMaxAge)
		caps.EditMaxAge = ptr.Ptr(jsontime.S(time.Duration(*meta.EditMaxAge) * time.Minute))
		if *meta.EditMaxAge == 0 {
			caps.Edit = event.CapLevelRejected
		}
	}
	if meta.AllowDelete != nil && !*meta.AllowDelete {
		if caps == roomCaps {
			caps = ptr.Clone(roomCaps)
		}
		caps.ID += "+disallow_delete"
		caps.Delete = event.CapLevelRejected
	}
	return caps
}
