// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"strings"
	"testing"
)

func TestPolicyModes(t *testing.T) {
	base := Policy{Enabled: true, Timezone: "UTC", KeepLast: 10, MinVerified: 1}
	cron := base
	cron.Cron = "0 2 * * *"
	if err := cron.Validate(); err != nil || cron.Adaptive() || cron.EffectiveCron() != "0 2 * * *" {
		t.Fatalf("cron policy: %v", err)
	}
	act := base
	act.Mode, act.CheckMinutes = ModeActivity, 15
	if err := act.Validate(); err != nil || !act.Adaptive() || act.EffectiveCron() != "*/15 * * * *" {
		t.Fatalf("activity policy: %v %q", err, act.EffectiveCron())
	}
	// Activity mode needs no cron expression from the user.
	if act.Cron != "" {
		t.Fatal("cron was set")
	}
	bad := act
	bad.CheckMinutes = 7
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "SQS-B033") {
		t.Fatalf("7 minutes accepted: %v", err)
	}
	bad = act
	bad.Mode = "random"
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "SQS-B034") {
		t.Fatalf("unknown mode accepted: %v", err)
	}
	if def := DefaultSettings("UTC").DefaultPolicy; !def.Adaptive() || def.CheckMinutes != 15 || def.Validate() != nil {
		t.Fatalf("default policy: %+v", def)
	}
}
