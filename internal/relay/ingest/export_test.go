// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest

// InjectSegmentRead replaces how an ingestor reads one segment, so a test can
// raise an I/O error that a file mode cannot produce.
func InjectSegmentRead(in *Ingestor, read func(path string) ([]byte, error)) { in.read = read }
