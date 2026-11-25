// Copyright (c) 2026 Canonical Ltd
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License version 3 as
// published by the Free Software Foundation.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package client

import (
	"context"
	"fmt"
)

// HealthOptions holds the options for a Health request.
type HealthOptions struct {
	// Level is the health level to check ("alive" or "ready"). If empty,
	// all checks are considered.
	Level string

	// Names is the list of check names to consider. If empty, all checks
	// are considered.
	Names []string
}

// HealthInfo holds the health information returned by the daemon.
type HealthInfo struct {
	Healthy bool `json:"healthy"`
}

// Health fetches the health of the daemon.
func (client *Client) Health(opts *HealthOptions) (*HealthInfo, error) {
	query := make(map[string][]string)
	if opts != nil {
		if opts.Level != "" {
			query["level"] = []string{opts.Level}
		}
		if len(opts.Names) > 0 {
			query["names"] = opts.Names
		}
	}

	var health HealthInfo
	resp, err := client.Requester().Do(context.Background(), &RequestOptions{
		Type:  SyncRequest,
		Path:  "/v1/health",
		Query: query,
	})
	if err != nil {
		return nil, err
	}
	if err := resp.DecodeResult(&health); err != nil {
		return nil, fmt.Errorf("cannot decode health result: %w", err)
	}
	return &health, nil
}
