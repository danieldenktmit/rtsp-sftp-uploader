// Package uploader publishes a local file to a remote SFTP server.
package uploader

import "context"

// Uploader publishes localPath to its configured remote destination.
// Implementations must be safe to call repeatedly and must honour ctx
// cancellation.
type Uploader interface {
	Upload(ctx context.Context, localPath string) error
}
