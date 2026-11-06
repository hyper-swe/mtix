// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package publisher_test

import (
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestPublishPending_RestoredInbox_PublishesOnlyLocalEvents(t *testing.T) {
	source := newRig(t)
	source.journalEvents(t, 1)
	require.NoError(t, source.store.AppendAnnotation(t.Context(), "PROJ-1", model.Annotation{ID: "private-mail", Author: "sender", Text: "private restored body", Addressee: "recipient"}))
	file, err := source.store.ExportWithInbox(t.Context(), "", "test")
	require.NoError(t, err)
	require.Len(t, file.InboxJournal.Events, 1)
	dest := newRig(t)
	_, err = dest.store.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	pub, err := publisher.New(dest.config())
	require.NoError(t, err)
	n, err := pub.PublishPending(t.Context())
	require.NoError(t, err)
	require.Zero(t, n, "restored mail never enters relay frames")
	require.Empty(t, dest.publishedEvents(t))
	dest.created = 1
	dest.journalEvents(t, 1)
	n, err = pub.PublishPending(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NotContains(t, dest.publishedEvents(t), file.InboxJournal.Events[0].EventID)
	require.Equal(t, dest.journalIDs(t), dest.publishedEvents(t))
}
