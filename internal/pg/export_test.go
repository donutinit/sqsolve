// SPDX-License-Identifier: AGPL-3.0-only

package pg

import "context"

// ExecTx exposes execTx to the integration tests.
func (c *Client) ExecTx(ctx context.Context, stmts ...string) (int, error) {
	return c.execTx(ctx, stmts...)
}
