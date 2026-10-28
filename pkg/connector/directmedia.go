// mautrix-slack - A Matrix-Slack puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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
	"fmt"
	"net/http"

	"github.com/slack-go/slack"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/mediaproxy"

	"go.mau.fi/mautrix-slack/pkg/slackid"
)

func (s *SlackConnector) Download(ctx context.Context, mediaID networkid.MediaID, _ map[string]string) (mediaproxy.GetMediaResponse, error) {
	rawParsedID, err := slackid.ParseMediaID(mediaID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse media ID: %w", err)
	}
	switch parsedID := rawParsedID.(type) {
	case *slackid.DirectMediaEmoji:
		return &mediaproxy.GetMediaResponseURL{
			URL: parsedID.URL(),
		}, nil
	case *slackid.DirectMediaFile:
		return s.downloadFile(ctx, parsedID)
	default:
		return nil, fmt.Errorf("unknown media ID type: %T", parsedID)
	}
}

func (s *SlackConnector) downloadFile(ctx context.Context, meta *slackid.DirectMediaFile) (mediaproxy.GetMediaResponse, error) {
	login, err := s.br.GetExistingUserLoginByID(ctx, meta.UserLoginID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user login: %w", err)
	} else if login == nil {
		return nil, mautrix.MNotFound.WithMessage("Direct media login not found")
	}
	client, ok := login.Client.(*SlackClient)
	if !ok || client.Client == nil {
		return nil, mautrix.MNotFound.WithMessage("Direct media login is not connected")
	}

	file, _, _, err := client.Client.GetFileInfoContext(ctx, meta.FileID, 0, 0)
	if err != nil {
		return nil, wrapSlackDownloadError(fmt.Errorf("failed to get file info: %w", err))
	}
	url := file.URLPrivateDownload
	if url == "" {
		url = file.URLPrivate
	}
	if url == "" {
		return nil, mautrix.MNotFound.WithMessage("File has no downloadable URL")
	}

	if file.IsExternal {
		return &mediaproxy.GetMediaResponseURL{URL: url}, nil
	}
	resp, err := client.Client.GetFileResponseContext(ctx, url)
	if err != nil {
		return nil, wrapSlackDownloadError(fmt.Errorf("failed to download file: %w", err))
	}
	return &mediaproxy.GetMediaResponseData{
		Reader:        resp.Body,
		ContentType:   file.Mimetype,
		ContentLength: max(resp.ContentLength, 0),
	}, nil
}

func wrapSlackDownloadError(err error) error {
	var mapped mautrix.RespError
	var statusErr slack.StatusCodeError
	if errors.As(err, &statusErr) {
		switch statusErr.Code {
		case http.StatusNotFound, http.StatusGone:
			mapped = mautrix.MNotFound.WithMessage("File not found on Slack")
		case http.StatusUnauthorized, http.StatusForbidden:
			mapped = mautrix.MForbidden.WithMessage("Slack denied access to the file")
		case http.StatusTooManyRequests:
			mapped = mautrix.MLimitExceeded.WithMessage("Slack is rate limiting file downloads").WithCanRetry(true)
		default:
			mapped = mautrix.MUnknown.WithMessage("Slack failed to serve the file").WithStatus(http.StatusBadGateway).WithCanRetry(true)
		}
		return fmt.Errorf("%w: %w", mapped, err)
	}
	switch slackAPIErrorCode(err) {
	case "":
		return err
	case "file_not_found", "file_deleted":
		mapped = mautrix.MNotFound.WithMessage("File not found on Slack")
	case "invalid_auth", "not_authed", "token_revoked", "token_expired", "account_inactive",
		"no_permission", "not_authorized", "access_denied", "missing_scope", "not_allowed_token_type",
		"team_access_not_granted", "ekm_access_denied":
		mapped = mautrix.MForbidden.WithMessage("Slack denied access to the file")
	case "ratelimited", "rate_limited":
		mapped = mautrix.MLimitExceeded.WithMessage("Slack is rate limiting file downloads").WithCanRetry(true)
	default:
		mapped = mautrix.MUnknown.WithMessage("Slack failed to serve the file").WithStatus(http.StatusBadGateway).WithCanRetry(true)
	}
	return fmt.Errorf("%w: %w", mapped, err)
}
