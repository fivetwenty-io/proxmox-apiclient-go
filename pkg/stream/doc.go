// Package stream provides a streaming response reader (Stream) for
// consuming a body as a sequence of decoded items — one JSON document, or
// newline-delimited JSON objects — via Read, ReadAll, ReadN, a channel
// (Channel), or a callback (Process/ProcessBatch), with a per-item size
// limit and basic throughput metrics (Metrics).
//
// This is a standalone opt-in utility: it is not wired into
// pkg/client.Client, which returns decoded response bodies directly rather
// than a Stream. Wrap an io.ReadCloser or *http.Response with
// stream.New / stream.NewFromResponse directly when your application needs
// to consume a long or incrementally-produced PVE response body item by
// item instead of buffering it whole.
package stream
