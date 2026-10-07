package ctl

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// DeletePack deletes a knowledge pack by name. The server refuses a pack
// that a role is still bound to, and its sentence names the roles to unbind
// first.
func (c *Client) DeletePack(ctx context.Context, name string) error {
	packs, err := c.Packs(ctx)
	if err != nil {
		return err
	}
	for _, p := range packs {
		if p.Name == name {
			return c.Do(ctx, http.MethodDelete, "/v1/admin/packs/"+url.PathEscape(p.ID), nil, nil)
		}
	}
	return fmt.Errorf("no pack named %q", name)
}
