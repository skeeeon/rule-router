# Changelog

## [0.7.0] - 2026-03-19
- Added `rule-scheduler` application for cron-based scheduled NATS publishing
- New `schedule` trigger type with standard 5-field cron expressions and optional IANA timezone support
- Schedule rules use the same conditions, templates, KV lookups, and time variables as NATS/HTTP rules
- Added `Publish()` method to NATS broker for direct publishing without a subscription manager
- Hot reload (`SIGHUP`) support for schedule rules
- Fixed KV `localCache.enabled` default not being applied when `kv.enabled: true` (defaulting logic ran before config was loaded)
- Prometheus metrics on port `:2114` for scheduler action tracking

## [0.6.0] - 2026-03-16
- Added optional per-rule debounce/throttle for triggers and actions
- Fire-first semantics: first message processed immediately, subsequent messages suppressed for the window duration
- Configurable time window (`debounce.window`) and template-based key (`debounce.key`) on NATS/HTTP triggers and actions
- Per-rule isolation via composite throttle keys prevents cross-rule interference
- New `throttle_suppressed_total` Prometheus metric with phase label (trigger/action)

## [0.5.0] - 2026-02-24
- Added `merge: true` action payload mode for NATS and HTTP actions
- Deep-merges a templated overlay onto the original message, preserving all existing fields
- Supports nested object recursion (nested maps merge rather than replace)
- Works with `forEach` (array element is the merge base)
- New `"merge"` action type metric for Prometheus observability

## [0.4.0] - 2025-11-09
- BREAKING CHANGE
- Variable syntax updated to require {} in all invocations
- Variable support added for condition values 

## [0.3.0] - 2025-11-05
- nats-auth-manager application for JWT lifecycle management

## [0.2.2] - 2025-11-03
- Enhanced Environment Variable Substitution 

## [0.2.1] - 2025-10-30
- Resolved primitive/array wrapping and resolution

## [0.2.0] - 2025-10-30
- Added primitive support for KV Lookups
- JSON path is option for KV Lookups

## [0.1.0] - 2025-10-29

### Added
- Initial release
- Rule-based NATS message routing
- Bidirectional HTTP gateway
- Array processing with forEach
- KV store integration with local cache
- Signature verification
- Rule-cli utility

[0.7.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.7.0
[0.1.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.1.0
[0.2.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.2.0
[0.2.1]: https://github.com/skeeeon/rule-router/releases/tag/v0.2.1
[0.2.2]: https://github.com/skeeeon/rule-router/releases/tag/v0.2.2
[0.3.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.3.0
[0.4.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.4.0
[0.5.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.5.0
[0.6.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.6.0
