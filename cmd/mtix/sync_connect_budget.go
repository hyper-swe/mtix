// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import "time"

// syncConnectBudget is how long a sync command gives the hub to accept a
// connection and answer: 30 s, long enough for a hub database that scales
// to zero to resume from idle. mtix sync init, clone, push and pull use it,
// and so does every hub check of mtix sync doctor, so the doctor passes
// whenever sync can connect (FR-18, MTIX-95.7).
const syncConnectBudget = 30 * time.Second
