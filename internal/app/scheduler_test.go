package app

import (
	"testing"

	"rule-router/config"
	"rule-router/internal/broker"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

// TestNewSchedulerApp_KVStartupJobsAreRebuildable pins that in KV mode the
// jobs registered at startup carry the tag rebuildCronJobs removes by. They
// used to be registered untagged, so a rule already in the bucket at startup
// survived every rebuild: deleting it from KV never stopped its job, and the
// first rebuild registered it a second time.
func TestNewSchedulerApp_KVStartupJobsAreRebuildable(t *testing.T) {
	log := logger.NewNop()
	processor := rule.NewProcessor(log)
	processor.ReplaceKVRuleSet(&rule.KVRuleSet{
		HTTP: &rule.HTTPKVRuleSet{Exact: map[string][]*rule.Rule{}},
		Schedule: []*rule.Rule{{
			Trigger: rule.Trigger{Schedule: &rule.ScheduleTrigger{Cron: "*/5 * * * *"}},
			Action:  rule.Action{NATS: &rule.NATSAction{Subject: "out.tick", Payload: "{}"}},
		}},
	})

	cfg := &config.Config{}
	cfg.KV.Rules.Enabled = true
	base := &BaseApp{
		Logger:        log,
		Processor:     processor,
		RuleKVManager: broker.NewRuleKVManager("rules", false, processor, nil, nil, log),
	}

	app, err := NewSchedulerApp(base, cfg)
	if err != nil {
		t.Fatalf("NewSchedulerApp: %v", err)
	}
	defer func() { _ = app.scheduler.Shutdown() }()

	if n := len(app.scheduler.Jobs()); n != 1 {
		t.Fatalf("startup registered %d jobs, want 1", n)
	}

	// The rule is deleted from KV, leaving no schedule rules.
	app.rebuildCronJobs(nil)
	if n := len(app.scheduler.Jobs()); n != 0 {
		t.Fatalf("%d jobs survived a rebuild with no schedule rules, want 0", n)
	}
}
