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
	"fmt"
	"io"

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
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}
	url := file.URLPrivateDownload
	if url == "" {
		url = file.URLPrivate
	}
	if url == "" {
		return nil, mautrix.MNotFound.WithMessage("File has no downloadable URL")
	}

	return &mediaproxy.GetMediaResponseCallback{
		ContentType:   file.Mimetype,
		ContentLength: int64(file.Size),
		Callback: func(w io.Writer) (int64, error) {
			cw := &countingWriter{Writer: w}
			return cw.n, client.Client.GetFileContext(ctx, url, cw)
		},
	}, nil
}

type countingWriter struct {
	io.Writer
	n int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.Writer.Write(p)
	cw.n += int64(n)
	return n, err
}
