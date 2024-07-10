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
	"errors"

	"github.com/rs/zerolog"
	"github.com/slack-go/slack"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

func (s *SlackClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	_, channelID := slackid.ParsePortalID(msg.Portal.ID)
	if channelID == "" {
		return nil, errors.New("invalid channel ID")
	}
	sendOpts, fileUpload, err := s.Main.MsgConv.ToSlack(ctx, msg.Portal, msg.Content, msg.Event, msg.ThreadRoot, nil)
	if err != nil {
		return nil, err
	}
	log := zerolog.Ctx(ctx)
	var timestamp string
	if sendOpts != nil {
		log.Debug().Msg("Sending message to Slack")
		_, timestamp, err = s.Client.PostMessageContext(ctx, channelID, slack.MsgOptionAsUser(true), sendOpts)
		if err != nil {
			return nil, err
		}
	} else if fileUpload != nil {
		log.Debug().Msg("Uploading attachment to Slack")
		file, err := s.Client.UploadFileContext(ctx, *fileUpload)
		if err != nil {
			log.Err(err).Msg("Failed to upload attachment to Slack")
			return nil, err
		}
		var shareInfo slack.ShareFileInfo
		// Slack puts the channel message info after uploading a file in either file.shares.private or file.shares.public
		if info, found := file.Shares.Private[channelID]; found && len(info) > 0 {
			shareInfo = info[0]
		} else if info, found = file.Shares.Public[channelID]; found && len(info) > 0 {
			shareInfo = info[0]
		} else {
			return nil, errors.New("failed to upload media to Slack")
		}
		timestamp = shareInfo.Ts
	}
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        slackid.MakeMessageID(s.TeamID, channelID, timestamp),
			SenderID:  slackid.MakeUserID(s.TeamID, s.UserID),
			Timestamp: slackid.ParseSlackTimestamp(timestamp),
		},
	}, nil
}
