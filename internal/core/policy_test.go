// SPDX-License-Identifier: AGPL-3.0-only

package core

import "testing"

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
	// SQS-B033 and SQS-B034 belong to DeleteBackup; the policy errors have their own codes.
	if err := bad.Validate(); err == nil || err.Error() != "SQS-B090 activity-based mode checks every 5, 10, 15, 30 or 60 minutes" {
		t.Fatalf("7 minutes accepted or wrong error: %v", err)
	}
	bad = act
	bad.Mode = "random"
	if err := bad.Validate(); err == nil || err.Error() != "SQS-B091 unknown schedule mode" {
		t.Fatalf("unknown mode accepted or wrong error: %v", err)
	}
	if def := DefaultSettings("UTC").DefaultPolicy; !def.Adaptive() || def.CheckMinutes != 15 || def.Validate() != nil {
		t.Fatalf("default policy: %+v", def)
	}
}
