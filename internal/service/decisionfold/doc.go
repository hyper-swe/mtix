// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package decisionfold folds the events of one decision request into the
// state of its card. The fold is a pure function of the event set and the
// delete set it is given: it reads no configuration, clock, hub stamp,
// acknowledgement state, arrival order or local node row, so every replica
// that holds the same events computes the same card.
package decisionfold
