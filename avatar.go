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

package main

import (
	"fmt"
	"io"
	"net/http"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"
)

func uploadPlainFile(intent *appservice.IntentAPI, url string) (id.ContentURI, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to prepare request: %w", err)
	}

	getResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to download avatar: %w", err)
	}

	data, err := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to read avatar data: %w", err)
	}

	mime := http.DetectContentType(data)
	resp, err := intent.UploadBytes(data, mime)
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to upload avatar to Matrix: %w", err)
	}

	return resp.ContentURI, nil
}
